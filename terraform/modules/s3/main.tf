# --- S3 Bucket for artifacts and outputs ---

resource "aws_s3_bucket" "storage" {
  bucket = "${var.project}-${var.environment}-storage-${var.account_id}"

  force_destroy = false
}

resource "aws_s3_bucket_versioning" "storage" {
  bucket = aws_s3_bucket.storage.id

  versioning_configuration {
    status = "Disabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "storage" {
  bucket = aws_s3_bucket.storage.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "storage" {
  bucket = aws_s3_bucket.storage.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Lifecycle: auto-delete artifacts after N days
resource "aws_s3_bucket_lifecycle_configuration" "storage" {
  bucket = aws_s3_bucket.storage.id

  rule {
    id     = "expire-artifacts"
    status = "Enabled"

    filter {
      prefix = "artifacts/"
    }

    expiration {
      days = var.artifacts_expiration_days
    }
  }

  rule {
    id     = "expire-outputs"
    status = "Enabled"

    filter {
      prefix = "outputs/"
    }

    expiration {
      days = var.outputs_expiration_days
    }
  }
}
