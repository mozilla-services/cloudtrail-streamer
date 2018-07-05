package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

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

func init() {
	if os.Getenv("CT_DEBUG_LOGGING") == "1" {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	mozlogrus.Enable("cloudtrail-streamer")
}

var (
	globalConfig Config
)

type Config struct {
	awsKinesisStream string // Kinesis stream for event submission
	awsKinesisRegion string // AWS region the kinesis stream exists in

	awsKinesisBatchSize int // Number of records in a batched put to the kinesis stream

	awsKinesisClient *kinesis.Kinesis

	awsSession *session.Session

	eventType string // Whether to use the S3 or SNS event handler. Default is S3.
}

func (c *Config) init() error {
	c.awsKinesisStream = os.Getenv("CT_KINESIS_STREAM")
	if c.awsKinesisStream == "" {
		return fmt.Errorf("CT_KINESIS_STREAM must be set")
	}

	c.awsKinesisRegion = os.Getenv("CT_KINESIS_REGION")
	if c.awsKinesisRegion == "" {
		return fmt.Errorf("CT_KINESIS_REGION must be set")
	}

	c.awsKinesisBatchSize = 500
	batchSize := os.Getenv("CT_KINESIS_BATCH_SIZE")
	if batchSize != "" {
		var err error
		c.awsKinesisBatchSize, err = strconv.Atoi(batchSize)
		if err != nil {
			log.Fatalf("Error converting CT_KINESIS_BATCH_SIZE (%v) to int: %s", batchSize, err)
		}

		if c.awsKinesisBatchSize > 500 {
			return fmt.Errorf("CT_KINESIS_BATCH_SIZE is set to a value greater than 500")
		}
	}

	c.awsSession = session.Must(session.NewSession())

	c.awsKinesisClient = kinesis.New(
		c.awsSession,
		aws.NewConfig().WithRegion(c.awsKinesisRegion),
	)

	c.eventType = "S3"
	eventType := os.Getenv("CT_EVENT_TYPE")
	if eventType != "" {
		c.eventType = eventType
	}
	if c.eventType != "S3" && c.eventType != "SNS" {
		return fmt.Errorf("CT_EVENT_TYPE is set to an invalid value, %s, must be either 'S3' or 'SNS'", eventType)
	}

	return nil
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
	_, err = blobBuf.ReadFrom(logFileBlob)
	if err != nil {
		log.Errorf("Error reading from logFileBlob: %s", err)
		return nil, err
	}

	var logFile CloudTrailFile
	err = json.Unmarshal(blobBuf.Bytes(), &logFile)
	if err != nil {
		return nil, err
	}

	return &logFile, nil
}

func putRecordsToKinesis(logfile *CloudTrailFile) error {
	var kRecordsBuf []*kinesis.PutRecordsRequestEntry

	for _, record := range logfile.Records {
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

		if len(kRecordsBuf) > 0 && len(kRecordsBuf)%globalConfig.awsKinesisBatchSize == 0 {
			_, err = globalConfig.awsKinesisClient.PutRecords(&kinesis.PutRecordsInput{
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
		_, err := globalConfig.awsKinesisClient.PutRecords(&kinesis.PutRecordsInput{
			Records:    kRecordsBuf,
			StreamName: aws.String(globalConfig.awsKinesisStream),
		})
		if err != nil {
			log.Errorf("Error pushing records to kinesis: %s", err)
			return err
		}
	}

	return nil
}

func streamS3ObjectToKinesis(awsRegion string, bucket string, objectKey string) error {
	s3Client := s3.New(
		globalConfig.awsSession,
		aws.NewConfig().WithRegion(awsRegion),
	)

	log.Debugf("Reading %s from %s", objectKey, bucket)
	object, err := fetchLogFromS3(s3Client, bucket, objectKey)
	if err != nil {
		return err
	}

	logFile, err := readLogFile(object)
	if err != nil {
		return err
	}

	err = putRecordsToKinesis(logFile)
	if err != nil {
		return err
	}

	return nil
}

func S3Handler(ctx context.Context, s3Event events.S3Event) error {
	log.Debugf("Handling S3 event: %v", s3Event)

	for _, s3Record := range s3Event.Records {
		err := streamS3ObjectToKinesis(
			s3Record.AWSRegion,
			s3Record.S3.Bucket.Name,
			s3Record.S3.Object.Key,
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func SNSHandler(ctx context.Context, snsEvent events.SNSEvent) error {
	log.Debugf("Handling SNS event: %v", snsEvent)

	for _, snsRecord := range snsEvent.Records {
		var s3Event events.S3Event
		err := json.Unmarshal([]byte(snsRecord.SNS.Message), &s3Event)
		if err != nil {
			return err
		}
		return S3Handler(ctx, s3Event)
	}

	return nil
}

func main() {
	err := globalConfig.init()
	if err != nil {
		log.Fatalf("Invalid config (%v): %s", globalConfig, err)
	}

	if globalConfig.eventType == "S3" {
		log.Debug("Starting S3Handler")
		lambda.Start(S3Handler)
	} else if globalConfig.eventType == "SNS" {
		log.Debug("Starting SNSHandler")
		lambda.Start(SNSHandler)
	} else {
		log.Fatalf("eventType (%s) is not set to either S3 or SNS.", globalConfig.eventType)
	}
}
