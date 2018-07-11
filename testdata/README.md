### example.json.gz

This file can be used to quickly test the S3 -> SNS -> Lambda -> Kinesis flow.
cloudtrail-streamer requires that the file is a gzipped json blob with a top-level
key named `Records` that is an array of objects, but does not inspect the contents
of each object.

Success will be seeing each object as an individual record in Kinesis.

Contents of `example.json`:
```json
{
  "Records": [
    {
      "event": 1
    },
    {
      "event": 2
    },
    {
      "event": 3
    }
  ]
}
```
