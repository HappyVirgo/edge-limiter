# Example root module. Supply real network IDs and an image through a
# terraform.tfvars file (git-ignored); no credentials live in this repo.
terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.40, < 7.0"
    }
  }
}

provider "aws" {
  region = var.region
}

variable "region" {
  description = "AWS region."
  type        = string
  default     = "us-east-1"
}

variable "vpc_id" {
  description = "Existing VPC ID."
  type        = string
}

variable "public_subnet_ids" {
  description = "Public subnets for the ALB."
  type        = list(string)
}

variable "private_subnet_ids" {
  description = "Private subnets for tasks and Redis."
  type        = list(string)
}

variable "container_image" {
  description = "edge-limiter image URI."
  type        = string
}

module "edge_limiter" {
  source = "../../modules/edge-limiter"

  vpc_id             = var.vpc_id
  public_subnet_ids  = var.public_subnet_ids
  private_subnet_ids = var.private_subnet_ids
  container_image    = var.container_image
  failure_mode       = "local"

  tags = {
    project = "edge-limiter"
  }
}

output "url" {
  description = "Base URL of the deployed service."
  value       = "http://${module.edge_limiter.alb_dns_name}"
}
