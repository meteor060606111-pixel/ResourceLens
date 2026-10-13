package main

/*
#cgo LDFLAGS: -framework UniformTypeIdentifiers
*/
import "C"

// Wails' native file dialogs use UTType; recent SDKs require its framework.
