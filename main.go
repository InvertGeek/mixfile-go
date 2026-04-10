package main

import (
	"fmt"
	"log"
	"mixfile-go/mixfile/server"

	"net/http"
)

//TIP <p>To run your code, right-click the code and select <b>Run</b>.</p> <p>Alternatively, click
// the <icon src="AllIcons.Actions.Execute"/> icon in the gutter and select the <b>Run</b> menu item from here.</p>

func main() {
	ser1 := &server.MixFileServer{
		HttpClient:        &http.Client{},
		DownloadTaskCount: 5,
	}
	fmt.Println("已启动服务器: 127.0.0.1:8080")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", ser1))
}
