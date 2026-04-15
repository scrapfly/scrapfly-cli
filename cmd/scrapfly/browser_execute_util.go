package main

import "encoding/base64"

func stdBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
