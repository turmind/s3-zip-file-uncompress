package main

import "github.com/aws/aws-lambda-go/events"

// S3Event which wrap an array of S3EventRecord
type S3EventAddTargetPath struct {
	Records    []events.S3EventRecord `json:"Records"`
	TargetPath string                 `json:"TargetPath"`
}
