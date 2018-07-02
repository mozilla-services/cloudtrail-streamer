package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/kinesis"
	"github.com/aws/aws-sdk-go/service/s3"

	log "github.com/sirupsen/logrus"
	"go.mozilla.org/mozlogrus"
)

var (
	globalConfig  Config
	kinesisClient *kinesis.Kinesis
)

type Config struct {
	awsKinesisStream string // Kinesis stream for event submission
	awsKinesisRegion string // AWS region the kinesis stream exists in

	awsSession *session.Session
}

func (c *Config) init() {
	c.awsKinesisStream = os.Getenv("CT_KINESIS_STREAM")
	c.awsKinesisRegion = os.Getenv("CT_KINESIS_REGION")

	c.awsSession = session.Must(session.NewSession())
}

func (c *Config) validate() error {
	if c.awsKinesisStream == "" {
		return fmt.Errorf("CT_KINESIS_STREAM must be set")
	}
	if c.awsKinesisRegion == "" {
		return fmt.Errorf("CT_KINESIS_REGION must be set")
	}

	return nil
}

func init() {
	if os.Getenv("CT_DEBUG_LOGGING") == "1" {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	log.SetFormatter(&mozlogrus.MozLogFormatter{
		LoggerName: "cloudtrail-streamer",
		Type:       "app.log",
	})
}

type CloudTrailFile struct {
	Records []map[string]interface{} `json:"Records"`
}

func fetchLogFromS3(s3Client *s3.S3, bucket string, objectKey string) (*s3.GetObjectOutput, error) {
	logInput := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	}

	object, err := s3Client.GetObject(logInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeNoSuchKey:
				log.Errorf("%s: %s", s3.ErrCodeNoSuchKey, aerr)
			default:
				log.Errorf("AWS Error: %s", aerr)
			}
			return nil, aerr
		}
		return nil, err
	}

	return object, nil
}

func readLogFile(object *s3.GetObjectOutput) (*CloudTrailFile, error) {
	defer object.Body.Close()
	logFileBlob, err := gzip.NewReader(object.Body)
	if err != nil {
		log.Errorf("Error unzipping cloudtrail json file: %s", err)
		return nil, err
	}

	blobBuf := new(bytes.Buffer)
	blobBuf.ReadFrom(logFileBlob)

	var logFile CloudTrailFile
	err = json.Unmarshal(blobBuf.Bytes(), &logFile)
	if err != nil {
		return nil, err
	}

	return &logFile, nil
}

func HandleRequest(ctx context.Context, s3Event events.S3Event) error {
	log.Debugf("Handling event: %v", s3Event)

	globalConfig.init()
	err := globalConfig.validate()
	if err != nil {
		log.Errorf("Invalid config (%v): %s", globalConfig, err)
		return err
	}

	kinesisClient = kinesis.New(
		globalConfig.awsSession,
		aws.NewConfig().WithRegion(globalConfig.awsKinesisRegion),
	)

	for _, s3Record := range s3Event.Records {
		s3Client := s3.New(
			globalConfig.awsSession,
			aws.NewConfig().WithRegion(s3Record.AWSRegion),
		)
		bucket := s3Record.S3.Bucket.Name
		objectKey := s3Record.S3.Object.Key

		log.Debugf("Reading %s from %s", objectKey, bucket)
		object, err := fetchLogFromS3(s3Client, bucket, objectKey)
		if err != nil {
			return err
		}

		logFile, err := readLogFile(object)
		if err != nil {
			return err
		}

		var kRecordsBuf []*kinesis.PutRecordsRequestEntry
		for _, record := range logFile.Records {
			log.Debugf("Writing record to kinesis: %v", record)
			encodedRecord, err := json.Marshal(record)
			if err != nil {
				log.Errorf("Error marshalling record (%v) to json: %s", record, err)
				continue
			}

			kRecordsBuf = append(kRecordsBuf, &kinesis.PutRecordsRequestEntry{
				Data:         encodedRecord,
				PartitionKey: aws.String("key"), //TODO: is this right?
			})

			if len(kRecordsBuf) > 0 && len(kRecordsBuf)%500 == 0 {
				_, err = kinesisClient.PutRecords(&kinesis.PutRecordsInput{
					Records:    kRecordsBuf,
					StreamName: aws.String(globalConfig.awsKinesisStream),
				})
				if err != nil {
					log.Errorf("Error pushing records to kinesis: %s", err)
					return err
				}

				kRecordsBuf = kRecordsBuf[:0]
			}
		}

		if len(kRecordsBuf) != 0 {
			_, err = kinesisClient.PutRecords(&kinesis.PutRecordsInput{
				Records:    kRecordsBuf,
				StreamName: aws.String(globalConfig.awsKinesisStream),
			})
			if err != nil {
				log.Errorf("Error pushing records to kinesis: %s", err)
				return err
			}
		}
	}

	return nil
}

func main() {
	lambda.Start(HandleRequest)
}
