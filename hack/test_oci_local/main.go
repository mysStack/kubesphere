package main

import (
	"flag"
	"fmt"
	"os"

	appv2 "kubesphere.io/api/application/v2"
	appclient "kubesphere.io/kubesphere/pkg/simple/client/application"
)

func main() {
	url := flag.String("url", "", "OCI repo URL, e.g. oci://host/repo")
	username := flag.String("username", "", "username for registry (optional)")
	password := flag.String("password", "", "password for registry (optional)")
	insecure := flag.Bool("insecure", true, "insecure skip tls verify (true by default for local testing)")
	plainHTTP := flag.Bool("plain-http", false, "force plain HTTP for OCI registry")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "usage: go run main.go -url oci://host/repo")
		os.Exit(2)
	}

	cred := appv2.RepoCredential{
		Username:  *username,
		Password:  *password,
		PlainHTTP: *plainHTTP,
	}
	if *insecure {
		b := true
		cred.InsecureSkipTLSVerify = &b
	}

	idx, err := appclient.LoadRepoIndex(*url, cred)
	if err != nil {
		fmt.Fprintf(os.Stderr, "LoadRepoIndex error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Index has %d entries\n", len(idx.Entries))
	for name, versions := range idx.Entries {
		for _, v := range versions {
			fmt.Printf("- %s %s -> %s\n", name, v.Version, v.Digest)
		}
	}
}
