//go:build linux

package main

import "unsafe"

func unsafePtrOf[T any](p *T) unsafe.Pointer { return unsafe.Pointer(p) }
