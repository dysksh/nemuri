# --- Dead Letter Queue ---

resource "aws_sqs_queue" "dlq" {
  name                      = "${var.project}-${var.environment}-jobs-dlq"
  message_retention_seconds = 1209600 # 14 days
}

# --- Job Queue ---

resource "aws_sqs_queue" "jobs" {
  name                       = "${var.project}-${var.environment}-jobs"
  visibility_timeout_seconds = 600   # 10 minutes
  message_retention_seconds  = 86400 # 1 day
  receive_wait_time_seconds  = 20    # long polling

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = 3
  })
}

# --- DLQ Redrive Allow Policy ---

resource "aws_sqs_queue_redrive_allow_policy" "dlq" {
  queue_url = aws_sqs_queue.dlq.id

  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.jobs.arn]
  })
}
