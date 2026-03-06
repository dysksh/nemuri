terraform {
  backend "s3" {
    bucket  = "nemuri-tfstate-188847976996"
    key     = "dev/terraform.tfstate"
    region  = "ap-northeast-1"
    profile = "default"
  }
}
