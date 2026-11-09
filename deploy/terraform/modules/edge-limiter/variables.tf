variable "name" {
  description = "Name prefix for every resource."
  type        = string
  default     = "edge-limiter"
}

variable "vpc_id" {
  description = "VPC to deploy into."
  type        = string
}

variable "public_subnet_ids" {
  description = "Subnets for the internet-facing ALB (at least two AZs)."
  type        = list(string)
}

variable "private_subnet_ids" {
  description = "Subnets for the Fargate tasks and ElastiCache (at least two AZs)."
  type        = list(string)
}

variable "container_image" {
  description = "edge-limiter image URI, e.g. <account>.dkr.ecr.<region>.amazonaws.com/edge-limiter:0.1.0."
  type        = string
}

variable "container_port" {
  description = "Port the service listens on inside the container."
  type        = number
  default     = 8080
}

variable "cpu" {
  description = "Fargate task CPU units."
  type        = number
  default     = 256
}

variable "memory" {
  description = "Fargate task memory (MiB)."
  type        = number
  default     = 512
}

variable "desired_count" {
  description = "Initial number of tasks."
  type        = number
  default     = 2
}

variable "min_count" {
  description = "Autoscaling lower bound."
  type        = number
  default     = 2
}

variable "max_count" {
  description = "Autoscaling upper bound."
  type        = number
  default     = 10
}

variable "failure_mode" {
  description = "Behaviour when Redis is unreachable: open, closed or local."
  type        = string
  default     = "local"

  validation {
    condition     = contains(["open", "closed", "local"], var.failure_mode)
    error_message = "failure_mode must be open, closed or local."
  }
}

variable "redis_node_type" {
  description = "ElastiCache node type."
  type        = string
  default     = "cache.t4g.micro"
}

variable "redis_replicas" {
  description = "Number of cache nodes (primary + replicas). Two or more enables automatic failover."
  type        = number
  default     = 2
}

variable "certificate_arn" {
  description = "ACM certificate for an HTTPS listener. Empty serves plain HTTP only."
  type        = string
  default     = ""
}

variable "ingress_cidrs" {
  description = "CIDR blocks allowed to reach the ALB."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "log_retention_days" {
  description = "CloudWatch Logs retention."
  type        = number
  default     = 14
}

variable "tags" {
  description = "Tags applied to every resource."
  type        = map(string)
  default     = {}
}
