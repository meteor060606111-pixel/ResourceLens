package main

/*
#cgo LDFLAGS: -framework Cocoa -framework WebKit
void ResourceLensInsetWebView(void);
*/
import "C"

func configureDesktopWindow() { C.ResourceLensInsetWebView() }
