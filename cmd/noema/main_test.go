package main

import "testing"

func TestVersionIsDefined(t *testing.T) {
    if version == "" {
        t.Fatal("versao nao definida")
    }
}
