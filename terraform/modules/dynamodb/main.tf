# --- DynamoDB Jobs Table ---

resource "aws_dynamodb_table" "jobs" {
  name         = "${var.project}-${var.environment}-jobs"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "job_id"

  attribute {
    name = "job_id"
    type = "S"
  }

  attribute {
    name = "thread_id"
    type = "S"
  }

  global_secondary_index {
    name            = "thread_id-index"
    hash_key        = "thread_id"
    projection_type = "ALL"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = false
  }
}
