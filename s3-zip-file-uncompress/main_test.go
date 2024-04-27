package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestS3Upload(t *testing.T) {
	data, _ := os.ReadFile("../events/s3-event.json")
	s3event := S3EventAddTargetPath{}
	json.Unmarshal(data, &s3event)
	LambdaHandler(context.TODO(), s3event)
}
