# Lambda Function for Cloudtrail to Kinesis streaming

This is a Lambda function that will stream Cloudtrail logs saved to an S3 bucket to
a single Kinesis stream.

It can support any number of S3 buckets, as it executes based off of any S3 notification
events sent either directly to the lambda func or to an SNS topic that the lambda func subscribes
to, but the code is specific to Cloudtrail logs. It will decode
the Cloudtrail JSON and send one "Record" at a time to the kinesis stream.

Any single lambda function running this code can only support EITHER S3 events or SNS events.
This is controlled by the `CT_EVENT_TYPE` environment variable, and defaults to S3.

## Lambda Packaging

`make package` can be used to package the function in a zip file. A docker container is
temporarily used to generate the Linux executable and archive it in the zip.

## Deployment

An example CloudFormation template exists in the [cf](./cf) directory. This will
create everything needed for the lambda function to function as well as a mock s3
bucket and kinesis stream that can be used for testing.

### Environment Variables

#### CT_KINESIS_STREAM (required)

The name of the Kinesis stream that Cloudtrail records will be pushed to.

Example: `CT_KINESIS_STREAM="cloudtrail-streamer"`

#### CT_KINESIS_REGION (required)

The region that the Kinesis stream lives in.

Example: `CT_KINESIS_REGION="us-west-2"`

#### CT_S3_ROLE_ARN (optional)

Role to assume for use by the s3 client.

Useful when this Lambda function and the S3 bucket with CloudTrail logs are in different AWS accounts.

Example: `CT_S3_ROLE_ARN="arn:aws:iam::555555555555:role/CloudtrailGetObjectRole"`

#### CT_EVENT_TYPE (optional)

The type of event that will be sent to the Lambda function. Default is `CT_EVENT_TYPE="S3"`.

To use the SNS event handler, set `CT_EVENT_TYPE="SNS"`.

#### CT_DEBUG_LOGGING (optional)

Setting `CT_DEBUG_LOGGING=1` will enable debug logging within the handler.

#### CT_KINESIS_BATCH_SIZE (optional)

The number of records in a batched put to the Kinesis stream.

By default, `CT_KINESIS_BATCH_SIZE` is set to `500` (which is the max allowed).

#### CT_EVENT_FILTERS (optional)

Comma-separated list of `eventSource:eventName` that will be filtered out.

Example: `CT_EVENT_FILTERS="kinesis:DescribeStream,elasticmapreduce:ListClusters"`

## References

The structure of this project is based off of this AWS tutorial:
https://docs.aws.amazon.com/lambda/latest/dg/with-cloudtrail.html
