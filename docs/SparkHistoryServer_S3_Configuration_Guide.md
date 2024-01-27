# S3 Configuration Guide for SparkHistoryServer


In order to use S3 as the storage for Spark event logs, you need to configure the spec.roleConfig.eventLog and spec.roleConfig.s3 sections in the SparkHistoryServer YAML configuration file.

Here is an example:

```yaml
roleConfig:
  eventLog:
    enabled: true
    mountMode: s3
    dir: /spark-tmp
  s3:
    endpoint: "http://bucket.example.com"
    enableSSL: false
    impl: "org.apache.hadoop.fs.s3a.S3AFileSystem"
    fastUpload: true
    accessKey: "accessKey"
    secretKey: "secret"
    pathStyleAccess: true
```

## Configuration Details

- `eventLog.enabled`: Set this to `true` to enable event logging.

- `eventLog.mountMode`: Set this to `s3` to use S3 as the storage for event logs.

- `eventLog.dir`: This is the directory where the event logs will be stored. In the case of S3, this will be the bucket name.

- `s3.endpoint`: The endpoint URL of your S3 service.

- `s3.enableSSL`: Set this to `true` to enable SSL for S3 connections. If your S3 service does not support SSL, set this to `false`.

- `s3.impl`: The implementation class for the S3 filesystem. For S3, this should be `org.apache.hadoop.fs.s3a.S3AFileSystem`.

- `s3.fastUpload`: Set this to `true` to enable fast upload.

- `s3.secretKey`: The secret key for your S3 service.

- `s3.pathStyleAccess`: Set this to `true` to enable path-style access.

Please replace the example values with your actual S3 configuration.
