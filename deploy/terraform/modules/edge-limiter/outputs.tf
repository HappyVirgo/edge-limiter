output "alb_dns_name" {
  description = "Public DNS name of the load balancer."
  value       = aws_lb.this.dns_name
}

output "service_name" {
  description = "ECS service name."
  value       = aws_ecs_service.this.name
}

output "cluster_name" {
  description = "ECS cluster name."
  value       = aws_ecs_cluster.this.name
}

output "redis_primary_endpoint" {
  description = "ElastiCache primary endpoint (TLS)."
  value       = aws_elasticache_replication_group.this.primary_endpoint_address
}

output "log_group_name" {
  description = "CloudWatch log group receiving the JSON logs."
  value       = aws_cloudwatch_log_group.this.name
}
