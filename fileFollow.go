package main

import (
	"os"
	"path"
	"net/http"
	"net/url"
	"runtime"
	"flag"
	"log"
	"sync"
	"sync/atomic"
	"io"
	"time"
)

var addr = flag.String("listen", ":80", "listen addr")
var base = flag.String("dir", ".", "Run on dir")
var follow = flag.String("follow", "", "Follow master server URL")
var syncMode = flag.String("sync", "none", "File Sync (none/lazy)")
var statusTime = flag.Int("info", 300, "Print a status message every N*second.")

var fileSystem http.Dir
var followURL url.URL
var status_time time.Duration
var notDelFile = false

//状态计数
var request_num int64
var inotify_event_num int64
var file_sync_num int64

func main() {
	flag.Parse()

	//初史化输入参数
	fileSystem = http.Dir(*base)
	status_time = time.Duration(*statusTime)

	if *follow != "" {
		_url, err := url.Parse(*follow)
		if err != nil {
			log.Fatal("follow", err)
		}
		if _url.Scheme == "" {
			_url.Scheme = "http"
		}
		followURL = *_url
	}

	runtime.GOMAXPROCS(runtime.NumCPU() * 2)

	//打印状态信息
	go requestStatus()

	if *follow == "" {
		log.Println("Run on master mode.")
		http.HandleFunc("/", masterServer)
	} else {
		log.Println("Run on slave mode. follow", followURL.String())

		switch *syncMode {
		case "none" :
			http.HandleFunc("/", proxyServer)
		case "lazy" :
			http.HandleFunc("/", syncServer)
		default:
			log.Fatal("Sync mode undefined!")
		}

		log.Println("File sync mode " + *syncMode + ".")
	}

	log.Fatal(http.ListenAndServe(*addr, nil))
}

//主服务器
func masterServer(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&request_num, 1)

	f, err := fileSystem.Open(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	serveContent(w, r, f)
}
//主服务器 结束

//直接转发请求
func proxyServer(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&request_num, 1)

	_url := followURL
	_url.Path = path.Join(_url.Path, r.URL.Path)
	_url.RawQuery = r.URL.RawQuery

	resp, err := http.Get(_url.String())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	defer resp.Body.Close()

    for k, v := range resp.Header {
        for _, vv := range v {
            w.Header().Add(k, vv)
        }
    }

	w.WriteHeader(resp.StatusCode)

	io.Copy(w, resp.Body)
}
//直接转发请求 结束

//带文件同步服务
func syncServer(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&request_num, 1)

	f, err := fileSystem.Open(r.URL.Path)
	if err != nil {
		if os.IsNotExist(err) {
			f, err = syncAndSaveFile(r)
			if err != nil {
				if os.IsNotExist(err) {
					http.NotFound(w, r)
				} else {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				return
			}
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	defer f.Close()

	serveContent(w, r, f)
}

func syncAndSaveFile(r *http.Request) (http.File, error) {
	name := path.Clean(r.URL.Path)

	//加锁
	l := getLocker(name)
	l.Lock()
	defer l.Unlock()

	//再次检查文件是否存在
	if f, err := fileSystem.Open(name); err == nil {
		return f, nil
	}

	_url := followURL
	_url.Path = path.Join(_url.Path, name)
	_url.RawQuery = r.URL.RawQuery

	resp, err := http.Get(_url.String())
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, os.ErrNotExist
	}

	//尝试获取文件最后更改时间
	t, _ := time.Parse(http.TimeFormat, resp.Header.Get("Last-Modified"))

	err = saveFile(name, t, resp.Body)
	if err != nil {
		return nil, err
	}

	return fileSystem.Open(name)
}

func saveFile(filename string, t time.Time, r io.Reader) error {
	atomic.AddInt64(&file_sync_num, 1)

	name := path.Join(string(fileSystem), filename)

	//创建文件夹
	dir := path.Dir(name)
	err := os.MkdirAll(dir, 0777)
	if err != nil {
		return err
	}

	//创建文件
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		os.Remove(name)
		return err
	}

	if err := f.Sync(); err != nil {
		os.Remove(name)
		return err
	}

	if t.Unix() > 0 {
		if err := os.Chtimes(name, t, t); err != nil {
			os.Remove(name)
			return err
		}
	}

	return nil
}
//带文件同步服务 结束

//输出状态信息
func requestStatus() {
	c := time.Tick(status_time * time.Second)

	for _ = range c {
		num1 := atomic.SwapInt64(&request_num, 0)
		num2 := atomic.SwapInt64(&file_sync_num, 0)

		if *follow == "" {
			log.Println("RequestNum:", num1)
		} else {
			if *syncMode == "none" {
				log.Println("RequestNum:", num1)
			} else {
				log.Println("RequestNum:", num1, "FileSyncNum:", num2)
			}
		}
	}
}

/** locker start **/
var lock sync.Mutex
var locks = make(map[string]*locker)

type locker struct{
	sync.Mutex
	num int
	name string
}

func getLocker(name string) *locker {
	lock.Lock()
	defer lock.Unlock()

	l, ok := locks[name]
	if !ok {
		l = new(locker)
		l.name = name
		locks[name] = l
	}

	l.num++

	return l
}

func (l *locker) UnLock() {
	lock.Lock()
	defer lock.Unlock()

	l.Mutex.Unlock()

	l.num--
	if l.num < 1 {
		delete(locks, l.name)
	}
}

/** locker end **/

func serveContent(w http.ResponseWriter, r *http.Request, f http.File) {
	d, err2 := f.Stat()
	if err2 != nil {
		http.Error(w, err2.Error(), http.StatusInternalServerError)
		return
	}

	//禁止访问文件夹
	if d.IsDir() {
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}

	http.ServeContent(w, r, d.Name(), d.ModTime(), f)
}
