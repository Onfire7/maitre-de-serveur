package main

import "syscall"

func sethostname(name string) error { return syscall.Sethostname([]byte(name)) }
