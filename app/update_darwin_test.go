package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMacBundleSwapAndVerificationRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "verification failure"}[fail], func(t *testing.T) {
			dir := t.TempDir()
			target, staged, previous := filepath.Join(dir, "ResourceLens.app"), filepath.Join(dir, "new"), filepath.Join(dir, "previous")
			os.Mkdir(target, 0700)
			os.Mkdir(staged, 0700)
			os.WriteFile(filepath.Join(target, "version"), []byte("old"), 0600)
			os.WriteFile(filepath.Join(staged, "version"), []byte("new"), 0600)
			err := replaceMacBundle(staged, target, previous, func(string) error {
				if fail {
					return errors.New("bad signature")
				}
				return nil
			})
			if fail != (err != nil) {
				t.Fatal("unexpected replacement result", err)
			}
			got, _ := os.ReadFile(filepath.Join(target, "version"))
			want := "new"
			if fail {
				want = "old"
			}
			if string(got) != want {
				t.Fatalf("installed %q, wanted %q", got, want)
			}
			if _, err := os.Stat(previous); !os.IsNotExist(err) {
				t.Fatal("previous app retained after success or restored failure")
			}
		})
	}
}
