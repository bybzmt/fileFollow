package main

import (
	"os"
	"path"
	"net/http"
	"runtime"
	"flag"
	"strings"
	"log"
	"sync"
	"io"
)

var addr = flag.String("addr", "", "listen addr")
var follow = flag.String("follow", "", "follow addr")
var base = flag.String("dir", ".", "Run on dir")
var syncfile = flag.String("sync", "off", "sync file on/off")

var fileSystem http.Dir

func main() {
	flag.Parse()

	fileSystem = http.Dir(*base)

	runtime.GOMAXPROCS(runtime.NumCPU())

	if *follow == "" {
		http.HandleFunc("/", masterServer)
	} else {
		//添加http
		if !strings.HasPrefix(*follow, "http") {
			*follow = "http://" + *follow
		}

		//去除未尾的/
		*follow = strings.TrimRight(*follow, "/")

		if *syncfile == "on" {
			http.HandleFunc("/", syncServer)
		} else {
			http.HandleFunc("/", proxyServer)
		}
	}

	log.Fatal(http.ListenAndServe(*addr, nil))
}

//主服务器
func masterServer(w http.ResponseWriter, r *http.Request) {
	f, err := fileSystem.Open(r.URL.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

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

//文件同步功能
func syncServer(w http.ResponseWriter, r *http.Request) {
	f, err := fileSystem.Open(r.URL.Path)
	if err != nil {
		if os.IsNotExist(err) {
			f, err = syncAndSendFile(r.URL.Path)
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

func syncAndSendFile(name string) (http.File, error) {
	name = path.Clean(name)

	//加锁
	l := getLocker(name)
	l.Lock()
	defer l.Unlock()

	//再次检查文件是否存在
	f, err := fileSystem.Open(name)
	if err == nil {
		return f, nil
	}

	_url := *follow + name

	resp, err := http.Get(_url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, os.ErrNotExist
	}

	return saveFile(resp.Body, name)
}

func saveFile(r io.Reader, filename string) (http.File, error) {
	name := path.Clean(*base + path.Clean("/" + filename))

	//创建文件夹
	dir := path.Dir(name)
	err := os.MkdirAll(dir, 0777)
	if err != nil {
		return nil, err
	}

	//创建文件
	f, err2 := os.Create(name)
	if err2 != nil {
		return nil, err
	}

	_, err = io.Copy(f, r)
	if err != nil {
		//删除错误的文件
		f.Close()
		os.Remove(name)
		return nil, err
	}

	err = f.Sync()
	if err != nil {
		//删除错误的文件
		f.Close()
		os.Remove(name)
		return nil, err
	}

	_, err = f.Seek(0, os.SEEK_SET)
	if err != nil {
		f.Close()
		return nil, err
	}

	return f, nil
}

//直接转发请求
func proxyServer(w http.ResponseWriter, r *http.Request) {
	_url := *follow + path.Clean("/" + r.URL.Path)

	resp, err := http.Get(_url)
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
