package main

import (
	"os"
	"path"
	"net/http"
	"net/url"
	"runtime"
	"flag"
	"strings"
	"log"
	"sync"
	"sync/atomic"
	"io"
	"time"
	"strconv"
	"container/list"
)

var addr = flag.String("addr", "", "listen addr")
var follow = flag.String("follow", "", "follow addr")
var base = flag.String("dir", ".", "Run on dir")
var syncfile = flag.String("sync", "off", "sync file on/off")
var followModifyTime = flag.String("followTime", "off", "follow master last modify time on/off")
var lastModifiedCacheLen = flag.Int("cache", 10000, "last modify time cache len")
var statusTime = flag.Int("info", 300, "Print Status Time (Second)")

var fileSystem http.Dir
var followURL url.URL

var request_num int64
var status_time time.Duration

func main() {
	flag.Parse()

	fileSystem = http.Dir(*base)

	runtime.GOMAXPROCS(runtime.NumCPU())

	status_time = time.Duration(*statusTime)

	if *follow == "" {
		http.HandleFunc("/", masterServer)
	} else {
		//添加http
		if !strings.HasPrefix(*follow, "http") {
			*follow = "http://" + *follow
		}

		_url, err := url.Parse(*follow)
		if err != nil {
			log.Fatal("follow", err)
		}
		followURL = *_url

		if *syncfile == "on" {
			if *followModifyTime == "on" {
				go lastModifiedCacheStatus()
			}
			http.HandleFunc("/", syncServer)
		} else {
			http.HandleFunc("/", proxyServer)
		}
	}

	go requestStatus()

	log.Fatal(http.ListenAndServe(*addr, nil))
}

//主服务器
func masterServer(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&request_num, 1)

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
	atomic.AddInt64(&request_num, 1)

	f, err := fileSystem.Open(r.URL.Path)
	if err != nil {
		if os.IsNotExist(err) {
			f, err = syncAndSendFile(r)
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

	//禁止访问文件夹 start
	d, err2 := f.Stat()
	if err2 != nil {
		http.Error(w, err2.Error(), http.StatusInternalServerError)
		return
	}

	if d.IsDir() {
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}
	//禁止访问文件夹 end

	basename := path.Base(r.URL.Path)
	var modtime time.Time

	if *followModifyTime == "on" {
		modtime, err = getMasterLastModified(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	} else {
		modtime = d.ModTime()
	}

	http.ServeContent(w, r, basename, modtime, f)
}

func syncAndSendFile(r *http.Request) (http.File, error) {
	name := path.Clean(r.URL.Path)

	//加锁
	l := getLocker(name)
	l.Lock()
	defer l.Unlock()

	//再次检查文件是否存在
	f, err := fileSystem.Open(name)
	if err == nil {
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

	var t2 *time.Time

	t, err := time.Parse(http.TimeFormat, resp.Header.Get("Last-Modified"))
	if  err == nil {
		if *followModifyTime == "on" {
			//缓存时间
			modifiedLock.Lock()
			modifiedCache.Add(name, t)
			modifiedLock.Unlock()
		}

		t2 = &t
	}

	return saveFile(resp.Body, name, t2)
}

func saveFile(r io.Reader, filename string, t *time.Time) (http.File, error) {
	name := path.Join(*base, filename)

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

	err = os.Chtimes(name, *t, *t)
	if err != nil {
		log.Println("Chtimes", err)
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

var modifiedLock sync.Mutex
var modifiedCache = newCache(*lastModifiedCacheLen)
var modifiedCacheHit int64
var modifiedCacheMiss int64

//读取主服器时间
func getMasterLastModified(r *http.Request) (t time.Time, err error) {
	name := path.Clean(r.URL.Path)

	//读缓存
	modifiedLock.Lock()
	tmp, has := modifiedCache.Get(name)
	modifiedLock.Unlock()
	if has {
		atomic.AddInt64(&modifiedCacheHit, 1)
		return tmp, nil
	}

	atomic.AddInt64(&modifiedCacheMiss, 1)

	_url := followURL
	_url.Path = path.Join(_url.Path, r.URL.Path)
	_url.RawQuery = r.URL.RawQuery

	resp, err2 := http.Head(_url.String())
	if err2 != nil {
		err = err2
		return
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		err = os.ErrNotExist
		return
	}

	t, err = time.Parse(http.TimeFormat, resp.Header.Get("Last-Modified"))

	if  err == nil {
		//缓存时间
		modifiedLock.Lock()
		modifiedCache.Add(name, t)
		modifiedLock.Unlock()
	}

	return
}

func lastModifiedCacheStatus() {
	c := time.Tick(status_time * time.Second)

	for _ = range c {
		hit := atomic.SwapInt64(&modifiedCacheHit, 0)
		miss := atomic.SwapInt64(&modifiedCacheMiss, 0)

		if hit > 0 || miss > 0 {
			log.Println("Cache hit/miss:", hit, miss, strconv.Itoa(int(float64(hit) / float64(hit+miss)*100))+"%")
		}
	}
}

func requestStatus() {
	c := time.Tick(status_time * time.Second)

	for _ = range c {
		num := atomic.SwapInt64(&request_num, 0)

		log.Println("Request Num:", num)
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

/** lru start ****/

type Cache struct {
	MaxEntries int
	ll    *list.List
	cache map[string]*list.Element
}

type entry struct {
	key   string
	value time.Time
}

func newCache(maxEntries int) *Cache {
	return &Cache{
		MaxEntries: maxEntries,
		ll:         list.New(),
		cache:      make(map[string]*list.Element),
	}
}

func (c *Cache) Add(key string, value time.Time) {
	if c.cache == nil {
		c.cache = make(map[string]*list.Element)
		c.ll = list.New()
	}
	if ee, ok := c.cache[key]; ok {
		c.ll.MoveToFront(ee)
		ee.Value.(*entry).value = value
		return
	}
	ele := c.ll.PushFront(&entry{key, value})
	c.cache[key] = ele
	if c.MaxEntries != 0 && c.ll.Len() > c.MaxEntries {
		c.RemoveOldest()
	}
}

func (c *Cache) Get(key string) (value time.Time, ok bool) {
	if c.cache == nil {
		return
	}
	if ele, hit := c.cache[key]; hit {
		c.ll.MoveToFront(ele)
		return ele.Value.(*entry).value, true
	}
	return
}

func (c *Cache) Remove(key string) {
	if c.cache == nil {
		return
	}
	if ele, hit := c.cache[key]; hit {
		c.removeElement(ele)
	}
}

func (c *Cache) RemoveOldest() {
	if c.cache == nil {
		return
	}
	ele := c.ll.Back()
	if ele != nil {
		c.removeElement(ele)
	}
}

func (c *Cache) removeElement(e *list.Element) {
	c.ll.Remove(e)
	kv := e.Value.(*entry)
	delete(c.cache, kv.key)
}

func (c *Cache) Len() int {
	if c.cache == nil {
		return 0
	}
	return c.ll.Len()
}

/** lru end ****/
