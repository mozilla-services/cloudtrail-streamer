# Lambda Function for Cloudtrail to Kinesis streaming

This is a Lambda function that will stream Cloudtrail logs saved to an S3 bucket to
a single Kinesis stream.

It can support any number of S3 buckets, as it executes based off of any S3 notification
event, but the code is specific to Cloudtrail logs. It will decode the Cloudtrail
JSON and send one "Record" at a time to the kinesis stream.

## Lambda Packaging

`make package` can be used to package the function in a zip file. A docker container is
temporarily used to generate the Linux executable and archive it in the zip.

## Deployment

An example CloudFormation template exists in the [cf](./cf) directory. This will
create everything needed for the lambda function to function as well as a mock s3
bucket and kinesis stream that can be used for testing.

### Environment Variables

#### CT_KINESIS_STREAM

The Kinesis stream that Cloudtrail records will be pushed to.

#### CT_KINESIS_REGION

The region that the Kinesis stream lives in.

#### CT_DEBUG_LOGGING

Setting `CT_DEBUG_LOGGING=1` will enable debug logging within the handler.

## References

The structure of this project is based off of this AWS tutorial:
https://docs.aws.amazon.com/lambda/latest/dg/with-cloudtrail.html
