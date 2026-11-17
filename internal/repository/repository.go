// Package repository defines the persistence layer abstractions for the
// KYMO service. Each interface is declared in its own file;
// this file contains shared request/response types used across repositories.
package repository

import "time"

// AggregatedResource holds pre-computed aggregate statistics for a container's
// resource usage over a requested time window. It is returned by
// ResourceRepository.GetAggregated and is suitable for dashboard queries that
// do not need raw per-sample data.
type AggregatedResource struct {
	// ContainerID identifies the container these statistics belong to.
	ContainerID string `json:"container_id"`

	// WindowStart is the inclusive start of the aggregation interval.
	WindowStart time.Time `json:"window_start"`

	// WindowEnd is the inclusive end of the aggregation interval.
	WindowEnd time.Time `json:"window_end"`

	// AvgCPUPercent is the mean CPU utilisation across all samples in the window.
	AvgCPUPercent float64 `json:"avg_cpu_percent"`

	// MaxCPUPercent is the peak CPU utilisation observed within the window.
	MaxCPUPercent float64 `json:"max_cpu_percent"`

	// AvgMemBytes is the mean resident memory usage in bytes across all samples.
	AvgMemBytes int64 `json:"avg_mem_bytes"`

	// MaxMemBytes is the peak resident memory usage in bytes within the window.
	MaxMemBytes int64 `json:"max_mem_bytes"`

	// TotalNetRxBytes is the total bytes received over the network in the window.
	TotalNetRxBytes int64 `json:"total_net_rx_bytes"`

	// TotalNetTxBytes is the total bytes transmitted over the network in the window.
	TotalNetTxBytes int64 `json:"total_net_tx_bytes"`
}
