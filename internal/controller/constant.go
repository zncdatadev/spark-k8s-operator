package controller

const s3CfgTemp = `
spark.hadoop.fs.s3a.endpoint %s
spark.hadoop.fs.s3a.ssl.enabled %t
spark.hadoop.fs.s3a.impl %s
spark.hadoop.fs.s3a.fast.upload %t
spark.hadoop.fs.s3a.path.style.access %t
`
const eventLogCfgTemp = `
spark.eventLog.enabled %t
spark.eventLog.dir %s
spark.history.fs.logDirectory %s
`

const historyCfgTemp = `
spark.history.fs.cleaner.enabled %t
spark.history.fs.cleaner.maxNum %d
spark.history.fs.cleaner.maxAge %s
spark.history.fs.eventLog.rolling.maxFilesToRetain %d
`
