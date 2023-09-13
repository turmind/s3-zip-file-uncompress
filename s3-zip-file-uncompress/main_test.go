package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestS3Upload(t *testing.T) {
	data, _ := os.ReadFile("../events/s3-event.json")
	s3event := events.S3Event{}
	json.Unmarshal(data, &s3event)
	LambdaHandler(context.TODO(), s3event)
}
