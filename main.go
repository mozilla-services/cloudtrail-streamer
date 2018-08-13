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
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
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
	awsKinesisStream    string // Kinesis stream for event submission
	awsKinesisRegion    string // AWS region the kinesis stream exists in
	awsKinesisBatchSize int    // Number of records in a batched put to the kinesis stream
	awsS3RoleArn        string // Optional Role to assume for S3 operations
	eventType           string // Whether to use the S3 or SNS event handler. Default is S3.

	awsKinesisClient *kinesis.Kinesis
	awsSession       *session.Session
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

		if c.awsKinesisBatchSize <= 0 || c.awsKinesisBatchSize > 500 {
			return fmt.Errorf("CT_KINESIS_BATCH_SIZE must be set to a value inbetween 1 and 500")
		}
	}

	c.awsS3RoleArn = os.Getenv("CT_S3_ROLE_ARN")

	c.eventType = "S3"
	eventType := os.Getenv("CT_EVENT_TYPE")
	if eventType != "" {
		c.eventType = eventType
	}
	if c.eventType != "S3" && c.eventType != "SNS" {
		return fmt.Errorf("CT_EVENT_TYPE is set to an invalid value, %s, must be either 'S3' or 'SNS'", eventType)
	}

	c.awsSession = session.Must(session.NewSession())

	c.awsKinesisClient = kinesis.New(
		c.awsSession,
		aws.NewConfig().WithRegion(c.awsKinesisRegion),
	)

	log.Debugf("Config: %v", c)
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

	log.Debugf("Calling GetObject with GetObjectInput: %+v", logInput)
	object, err := s3Client.GetObject(logInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			log.Errorf("AWS Error: %s", aerr)
			return nil, aerr
		}
		log.Errorf("Error getting S3 object: %s", err)
		return nil, err
	}

	if object.ContentEncoding != nil {
		log.Debugf("Obj ContentEncoding: %s", *object.ContentEncoding)
	} else {
		log.Debugf("Obj ContentEncoding is nil")
	}
	if object.ContentType != nil {
		log.Debugf("Obj ContentType: %s", *object.ContentType)
	} else {
		log.Debugf("Obj ContentType is nil")
	}
	if object.ContentLength != nil {
		log.Debugf("Obj ContentLength: %d", *object.ContentLength)
	} else {
		log.Debugf("Obj ContentLength is nil")
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
	defer logFileBlob.Close()

	blobBuf := new(bytes.Buffer)
	_, err = blobBuf.ReadFrom(logFileBlob)
	if err != nil {
		log.Errorf("Error reading from logFileBlob: %s", err)
		return nil, err
	}

	var logFile CloudTrailFile
	err = json.Unmarshal(blobBuf.Bytes(), &logFile)
	if err != nil {
		log.Errorf("Error unmarshalling s3 object to CloudTrailFile: %s", err)
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
	// log.Debugf("Session Config: %+v", globalConfig.awsSession.Config)
	log.Debugf("Session Credentials: %+v", globalConfig.awsSession.Config.Credentials)

	s3ClientConfig := aws.NewConfig().WithRegion(awsRegion)
	if globalConfig.awsS3RoleArn != "" {
		creds := stscreds.NewCredentials(globalConfig.awsSession, globalConfig.awsS3RoleArn)
		s3ClientConfig.Credentials = creds
		// log.Debugf("STS Credentials: %v", creds)
		log.Debugf("S3 client config: %v", s3ClientConfig)
	}
	s3Client := s3.New(globalConfig.awsSession, s3ClientConfig)

	log.Debugf("Reading %s from %s with client config of %+v", objectKey, bucket, s3Client.Config)
	creds, err := s3Client.Config.Credentials.Get()
	if err != nil {
		log.Errorf("Error getting credentials from s3Client.Config. Err: %s", err)
	}
	log.Debugf("Client Credentials: %v", creds)

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
	log.Debugf("Received context: %+v", ctx)
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
	log.Debugf("Handling SNS event: %+v", snsEvent)

	for _, snsRecord := range snsEvent.Records {
		var s3Event events.S3Event
		err := json.Unmarshal([]byte(snsRecord.SNS.Message), &s3Event)
		if err != nil {
			return err
		}

		err = S3Handler(ctx, s3Event)
		if err != nil {
			return err
		}
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
