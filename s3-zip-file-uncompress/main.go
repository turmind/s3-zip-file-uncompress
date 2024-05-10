package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	archiver "github.com/mholt/archiver/v4"
)

var sess *session.Session
var threadLimit int
var unCompressBucket string

const (
	RETRY_LIMIT        = 100
	LOCAL_PROCESS_PATH = "/tmp/process"
)

func init() {
	threadNumber := os.Getenv("THREAD_NUMBER")
	var err error
	threadLimit, err = strconv.Atoi(threadNumber)
	if err != nil {
		threadLimit = 100
	}
	unCompressBucket = os.Getenv("UNCOMPRESS_BUCKET")
	if unCompressBucket == "" {
		panic("UNCOMPRESS_BUCKET is empty")
	}
}

func LambdaHandler(context context.Context, s3Event S3EventAddTargetPath) (message string, err error) {
	if len(s3Event.Records) > 1 {
		log.Panic("receiver s3 records more than 1 in an event")
	}
	totalFileNumber := 0
	uploadFileNumber := int32(0)
	for _, record := range s3Event.Records {
		sess, _ = session.NewSession(&aws.Config{Region: aws.String(record.AWSRegion)})
		s3record := record.S3
		uploadInfo := fmt.Sprintf("[%s - %s] Bucket = %s, Key = %s \n", record.EventSource, record.EventTime, s3record.Bucket.Name, s3record.Object.Key)
		log.Println(uploadInfo)

		// recreate /tmp/process folder
		if err = os.RemoveAll(LOCAL_PROCESS_PATH); err != nil {
			log.Panic("remove local tmp files fail: ", err)
		}
		if err = os.MkdirAll(LOCAL_PROCESS_PATH, 0755); err != nil {
			log.Panic("create local tmp files fail: ", err)
		}
		// get s3 upload prefix
		prefix := s3record.Object.Key[:strings.LastIndex(s3record.Object.Key, "/")+1]
		if s3Event.TargetPath != "" {
			prefix = strings.TrimPrefix(s3Event.TargetPath, "/")
			if !strings.HasSuffix(prefix, "/") {
				prefix += "/"
			}
			if prefix == "/" {
				prefix = ""
			}
		}
		fileName := s3record.Object.Key[strings.LastIndex(s3record.Object.Key, "/")+1:]
		localPath := LOCAL_PROCESS_PATH + "/" + fileName

		// download file from s3
		if err = download(s3record.Bucket.Name, localPath, s3record.Object.Key); err != nil {
			log.Panic("download file fail: ", err)
		}
		log.Println("download zip file success")

		// uncompress file
		files, err := uncompress(localPath, LOCAL_PROCESS_PATH+"/uncompress")
		if err != nil {
			log.Panic("uncompress file fail: ", err)
		}
		totalFileNumber += len(files)
		log.Println("uncompress file success")

		// upload file to s3
		rate := make(chan int, threadLimit)
		var wg sync.WaitGroup
		for _, path := range files {
			rate <- 1
			wg.Add(1)
			go func(path string, prefix string) {
				defer wg.Done()
				defer func() {
					<-rate
				}()
				for i := 0; i < 100; i++ {
					if err := upload(unCompressBucket, path, prefix); err == nil {
						atomic.AddInt32(&uploadFileNumber, 1)
						break
					} else {
						time.Sleep(1 * time.Second)
						if i == 99 {
							log.Panic("upload file fail: ", err)
						}
					}

				}
			}(path, prefix)
		}
		wg.Wait()
	}

	log.Printf("upload file success, total file upload: %d, success: %d", totalFileNumber, uploadFileNumber)
	message = fmt.Sprintf("total file upload: %d, success: %d\n", totalFileNumber, uploadFileNumber)
	return message, nil
}

// download file from s3
func download(bucket string, localPath string, key string) error {
	downloader := s3manager.NewDownloader(sess)
	file, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = downloader.Download(file,
		&s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
	if err != nil {
		return err
	}
	return nil
}

// uncompress file to dest, return file list
func uncompress(src, dest string) (files []string, err error) {
	os.MkdirAll(dest, 0755)
	files = make([]string, 0, 10)
	input, err := os.Open(src)
	if err != nil {
		return
	}
	defer input.Close()
	format, _, err := archiver.Identify(src, input)
	if err != nil {
		return
	}
	ex, ok := format.(archiver.Extractor)
	if !ok {
		return nil, errors.New("format not support extract")
	}
	handler := func(ctx context.Context, f archiver.File) error {
		rc, err := f.Open()
		if err != nil {
			log.Fatal(err)
			return err
		}
		fpath := filepath.Join(dest, f.NameInArchive)
		if f.FileInfo.IsDir() {
			err := os.MkdirAll(fpath, f.FileInfo.Mode()|0100)
			if err != nil {
				log.Fatal(err)
				return err
			}
		} else {
			files = append(files, fpath)
			var fdir string
			if lastIndex := strings.LastIndex(fpath, string(os.PathSeparator)); lastIndex > -1 {
				fdir = fpath[:lastIndex]
			}
			err = os.MkdirAll(fdir, f.FileInfo.Mode()|0100)
			if err != nil {
				log.Fatal(err)
				return err
			}
			localFile, err := os.OpenFile(
				fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo.Mode())
			if err != nil {
				log.Fatal(err)
				return err
			}
			_, err = io.Copy(localFile, rc)
			if err != nil {
				log.Fatal(err)
				return err
			}
			localFile.Close()
		}
		rc.Close()
		return nil
	}
	input.Seek(0, 0)
	err = ex.Extract(context.TODO(), input, nil, handler)
	return
}

// upload file to s3
func upload(bucket string, path string, prefix string) error {
	uploader := s3manager.NewUploader(sess)
	file, err := os.Open(path)
	if err != nil {
		log.Println("Failed opening file", path, err)
		return err
	}
	_, err = uploader.Upload(&s3manager.UploadInput{
		Bucket: &bucket,
		Key:    aws.String(prefix + strings.Replace(path, "/tmp/process/uncompress/", "", 1)),
		Body:   file,
	})
	if err != nil {
		log.Println("Failed upload file", path, err)
		file.Close()
		return err
	}
	file.Close()
	return nil
}

func main() {
	lambda.Start(LambdaHandler)
}
