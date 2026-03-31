package main

import "sync"

var (
	globalCookie string = "" // 存储全局Cookie字符串
	mu           sync.Mutex
)

func SetGlobalCookie(cookie string) {
	mu.Lock()
	defer mu.Unlock()
	globalCookie = cookie
}

func GetGlobalCookie() string {
	mu.Lock()
	defer mu.Unlock()
	return globalCookie
}
