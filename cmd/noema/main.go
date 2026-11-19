package main

import (
    "fmt"
    "os"
)

const version = "0.1.0-bootstrap"

func main() {
    if len(os.Args) > 1 && os.Args[1] == "version" {
        fmt.Println("noema", version)
        return
    }
    fmt.Println("Noema: estrutura inicial. Consulte docs/ROADMAP.md antes de implementar a pipeline.")
}
