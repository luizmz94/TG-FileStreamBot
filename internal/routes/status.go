package routes

import (
	"EverythingSuckz/fsb/internal/bot"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// LoadStatus registers the status monitoring route
// This route provides real-time metrics for all workers including load, uptime, and performance
func (e *allRoutes) LoadStatus(r *Route) {
	statusLog := e.log.Named("Status")
	defer statusLog.Info("Loaded status route")
	r.Engine.GET("/status", getStatusRoute(statusLog))
}

type WorkerStatus struct {
	ID                int     `json:"id"`
	Username          string  `json:"username"`
	ActiveRequests    int32   `json:"active_requests"`
	TotalRequests     int64   `json:"total_requests"`
	FailedRequests    int64   `json:"failed_requests"`
	SuccessRate       float64 `json:"success_rate"`
	AverageResponseMs float64 `json:"average_response_ms"`
	UptimeSeconds     int64   `json:"uptime_seconds"`
	LastRequestAgo    string  `json:"last_request_ago"`
}

type StatusResponse struct {
	TotalWorkers       int            `json:"total_workers"`
	TotalActiveReqs    int32          `json:"total_active_requests"`
	TotalRequests      int64          `json:"total_requests"`
	TotalFailedReqs    int64          `json:"total_failed_requests"`
	OverallSuccessRate float64        `json:"overall_success_rate"`
	Workers            []WorkerStatus `json:"workers"`
	Timestamp          time.Time      `json:"timestamp"`
}

func getStatusRoute(logger *zap.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if bot.Workers == nil || len(bot.Workers.Bots) == 0 {
			// Check if request wants HTML
			if ctx.GetHeader("Accept") == "text/html" || ctx.Query("format") == "html" {
				ctx.Data(http.StatusOK, "text/html; charset=utf-8", []byte(getNoWorkersHTML()))
				return
			}
			ctx.JSON(http.StatusOK, gin.H{
				"message": "No workers available",
				"workers": []WorkerStatus{},
			})
			return
		}

		var totalActiveReqs int32
		var totalRequests int64
		var totalFailedReqs int64
		workers := make([]WorkerStatus, 0, len(bot.Workers.Bots))

		now := time.Now()

		for _, worker := range bot.Workers.Bots {
			metrics := worker.GetMetrics()

			totalActiveReqs += metrics.ActiveRequests
			totalRequests += metrics.TotalRequests
			totalFailedReqs += metrics.FailedRequests

			// Calculate success rate
			successRate := 0.0
			if metrics.TotalRequests > 0 {
				successfulReqs := metrics.TotalRequests - metrics.FailedRequests
				successRate = (float64(successfulReqs) / float64(metrics.TotalRequests)) * 100
			}

			// Calculate uptime
			uptime := now.Sub(metrics.StartTime).Seconds()

			// Calculate time since last request
			lastRequestAgo := "never"
			if !metrics.LastRequestTime.IsZero() {
				duration := now.Sub(metrics.LastRequestTime)
				if duration < time.Minute {
					lastRequestAgo = duration.Round(time.Second).String()
				} else if duration < time.Hour {
					lastRequestAgo = duration.Round(time.Minute).String()
				} else {
					lastRequestAgo = duration.Round(time.Hour).String()
				}
			}

			workers = append(workers, WorkerStatus{
				ID:                worker.ID,
				Username:          worker.Self.Username,
				ActiveRequests:    metrics.ActiveRequests,
				TotalRequests:     metrics.TotalRequests,
				FailedRequests:    metrics.FailedRequests,
				SuccessRate:       successRate,
				AverageResponseMs: worker.GetAverageResponseTime(),
				UptimeSeconds:     int64(uptime),
				LastRequestAgo:    lastRequestAgo,
			})
		}

		// Calculate overall success rate
		overallSuccessRate := 0.0
		if totalRequests > 0 {
			successfulReqs := totalRequests - totalFailedReqs
			overallSuccessRate = (float64(successfulReqs) / float64(totalRequests)) * 100
		}

		response := StatusResponse{
			TotalWorkers:       len(bot.Workers.Bots),
			TotalActiveReqs:    totalActiveReqs,
			TotalRequests:      totalRequests,
			TotalFailedReqs:    totalFailedReqs,
			OverallSuccessRate: overallSuccessRate,
			Workers:            workers,
			Timestamp:          now,
		}

		// Check if browser is requesting (wants HTML)
		acceptHeader := ctx.GetHeader("Accept")
		if ctx.Query("format") == "html" || (acceptHeader != "" &&
			(ctx.GetHeader("Accept") == "text/html" ||
				ctx.GetHeader("User-Agent") != "" && len(acceptHeader) > 0)) {
			// Return HTML table view
			htmlContent := generateStatusHTML(response)
			ctx.Data(http.StatusOK, "text/html; charset=utf-8", []byte(htmlContent))
			return
		}

		// Return JSON for API calls
		ctx.JSON(http.StatusOK, response)
	}
}

func getNoWorkersHTML() string {
	return `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<meta http-equiv="refresh" content="1">
	<title>Workers Status</title>
	<style>
		body {
			font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
			margin: 0;
			padding: 20px;
			background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
			min-height: 100vh;
		}
		.container {
			max-width: 1400px;
			margin: 0 auto;
			background: white;
			border-radius: 12px;
			padding: 30px;
			box-shadow: 0 20px 60px rgba(0,0,0,0.3);
		}
		h1 {
			color: #2d3748;
			margin-top: 0;
			text-align: center;
		}
		.error {
			text-align: center;
			color: #e53e3e;
			font-size: 18px;
			padding: 40px;
		}
	</style>
</head>
<body>
	<div class="container">
		<h1>⚠️ Workers Status</h1>
		<div class="error">No workers available</div>
	</div>
</body>
</html>`
}

func generateStatusHTML(response StatusResponse) string {
	// Sort workers by ID
	sort.Slice(response.Workers, func(i, j int) bool {
		return response.Workers[i].ID < response.Workers[j].ID
	})

	// Generate worker rows
	workerRows := ""
	for _, worker := range response.Workers {
		// Determine status color based on active requests
		statusClass := "status-idle"
		statusIcon := "🟢"
		if worker.ActiveRequests > 5 {
			statusClass = "status-busy"
			statusIcon = "🔴"
		} else if worker.ActiveRequests > 0 {
			statusClass = "status-active"
			statusIcon = "🟡"
		}

		// Format uptime
		uptimeStr := formatUptime(worker.UptimeSeconds)

		workerRows += fmt.Sprintf(`
		<tr class="%s">
			<td><strong>#%d</strong></td>
			<td>%s @%s</td>
			<td class="active-reqs">%d</td>
			<td>%d</td>
			<td>%d</td>
			<td class="success-rate">%.1f%%</td>
			<td>%.0f ms</td>
			<td>%s</td>
			<td>%s</td>
		</tr>`, statusClass, worker.ID, statusIcon, worker.Username,
			worker.ActiveRequests, worker.TotalRequests, worker.FailedRequests,
			worker.SuccessRate, worker.AverageResponseMs, uptimeStr, worker.LastRequestAgo)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>Workers Status - Real-time Dashboard</title>
	<style>
		* {
			margin: 0;
			padding: 0;
			box-sizing: border-box;
		}
		body {
			font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
			background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
			min-height: 100vh;
			padding: 20px;
		}
		.container {
			max-width: 1600px;
			margin: 0 auto;
			background: white;
			border-radius: 12px;
			padding: 30px;
			box-shadow: 0 20px 60px rgba(0,0,0,0.3);
		}
		h1 {
			color: #2d3748;
			margin-bottom: 10px;
			text-align: center;
			font-size: 32px;
		}
		.subtitle {
			text-align: center;
			color: #718096;
			margin-bottom: 20px;
			font-size: 14px;
		}
		.controls {
			display: flex;
			justify-content: center;
			align-items: center;
			gap: 15px;
			margin-bottom: 30px;
			padding: 15px;
			background: #f7fafc;
			border-radius: 8px;
		}
		.control-group {
			display: flex;
			align-items: center;
			gap: 10px;
		}
		.control-label {
			font-size: 14px;
			color: #4a5568;
			font-weight: 500;
		}
		.switch {
			position: relative;
			display: inline-block;
			width: 50px;
			height: 26px;
		}
		.switch input {
			opacity: 0;
			width: 0;
			height: 0;
		}
		.slider {
			position: absolute;
			cursor: pointer;
			top: 0;
			left: 0;
			right: 0;
			bottom: 0;
			background-color: #cbd5e0;
			transition: .4s;
			border-radius: 26px;
		}
		.slider:before {
			position: absolute;
			content: "";
			height: 20px;
			width: 20px;
			left: 3px;
			bottom: 3px;
			background-color: white;
			transition: .4s;
			border-radius: 50%%;
		}
		input:checked + .slider {
			background-color: #48bb78;
		}
		input:checked + .slider:before {
			transform: translateX(24px);
		}
		.stats-grid {
			display: grid;
			grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
			gap: 20px;
			margin-bottom: 30px;
		}
		.stat-card {
			background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
			color: white;
			padding: 20px;
			border-radius: 8px;
			box-shadow: 0 4px 6px rgba(0,0,0,0.1);
		}
		.stat-card h3 {
			font-size: 14px;
			font-weight: 500;
			margin-bottom: 8px;
			opacity: 0.9;
		}
		.stat-card .value {
			font-size: 32px;
			font-weight: bold;
		}
		.table-container {
			overflow-x: auto;
			border-radius: 8px;
			border: 1px solid #e2e8f0;
		}
		table {
			width: 100%%;
			border-collapse: collapse;
			background: white;
		}
		th {
			background: #f7fafc;
			color: #2d3748;
			font-weight: 600;
			text-align: left;
			padding: 12px 16px;
			border-bottom: 2px solid #e2e8f0;
			font-size: 13px;
			text-transform: uppercase;
			letter-spacing: 0.5px;
		}
		td {
			padding: 12px 16px;
			border-bottom: 1px solid #e2e8f0;
			font-size: 14px;
		}
		tr:hover {
			background: #f7fafc;
		}
		.status-idle {
			background: #f0fff4;
		}
		.status-active {
			background: #fefcbf;
		}
		.status-busy {
			background: #fed7d7;
		}
		.active-reqs {
			font-weight: bold;
			color: #2b6cb0;
		}
		.success-rate {
			font-weight: 600;
		}
		.timestamp {
			text-align: center;
			color: #718096;
			margin-top: 20px;
			font-size: 12px;
		}
		.auto-refresh {
			text-align: center;
			color: #48bb78;
			margin-top: 8px;
			font-size: 12px;
			font-weight: 600;
		}
		@keyframes blink {
			0%%, 100%% { opacity: 1; }
			50%% { opacity: 0.3; }
		}
		.blink {
			animation: blink 1s ease-in-out infinite;
		}
	</style>
</head>
<body>
	<div class="container">
		<h1>🤖 Workers Status Dashboard</h1>
		<div class="subtitle">Real-time monitoring</div>
		
		<div class="controls">
			<div class="control-group">
				<span class="control-label">Auto-refresh (1s):</span>
				<label class="switch">
					<input type="checkbox" id="autoRefreshToggle" checked>
					<span class="slider"></span>
				</label>
			</div>
			<div class="control-group">
				<span id="refreshStatus" class="control-label" style="color: #48bb78;">
					<span class="blink">●</span> Active
				</span>
			</div>
		</div>
		
		<div class="stats-grid">
			<div class="stat-card">
				<h3>Total Workers</h3>
				<div class="value">%d</div>
			</div>
			<div class="stat-card">
				<h3>Active Requests</h3>
				<div class="value">%d</div>
			</div>
			<div class="stat-card">
				<h3>Total Requests</h3>
				<div class="value">%d</div>
			</div>
			<div class="stat-card">
				<h3>Success Rate</h3>
				<div class="value">%.1f%%%%</div>
			</div>
		</div>

		<div class="table-container">
			<table>
				<thead>
					<tr>
						<th>ID</th>
						<th>Bot</th>
						<th>Active</th>
						<th>Total</th>
						<th>Failed</th>
						<th>Success Rate</th>
						<th>Avg Response</th>
						<th>Uptime</th>
						<th>Last Request</th>
					</tr>
				</thead>
				<tbody>
					%s
				</tbody>
			</table>
		</div>

		<div class="timestamp">Last updated: %s</div>
	</div>

	<script>
		let refreshTimer = null;
		let isAutoRefreshEnabled = true;

		const toggle = document.getElementById('autoRefreshToggle');
		const statusText = document.getElementById('refreshStatus');

		function updateStatus() {
			if (isAutoRefreshEnabled) {
				statusText.innerHTML = '<span class="blink">●</span> Active';
				statusText.style.color = '#48bb78';
			} else {
				statusText.innerHTML = '○ Paused';
				statusText.style.color = '#e53e3e';
			}
		}

		function startAutoRefresh() {
			if (refreshTimer) {
				clearTimeout(refreshTimer);
			}
			if (isAutoRefreshEnabled) {
				refreshTimer = setTimeout(function() {
					location.reload();
				}, 1000);
			}
		}

		toggle.addEventListener('change', function() {
			isAutoRefreshEnabled = this.checked;
			updateStatus();
			if (isAutoRefreshEnabled) {
				startAutoRefresh();
			} else {
				if (refreshTimer) {
					clearTimeout(refreshTimer);
					refreshTimer = null;
				}
			}
		});

		// Start auto-refresh on page load
		startAutoRefresh();
	</script>
</body>
</html>`,
		response.TotalWorkers,
		response.TotalActiveReqs,
		response.TotalRequests,
		response.OverallSuccessRate,
		workerRows,
		response.Timestamp.Format("2006-01-02 15:04:05"))
}

func formatUptime(seconds int64) string {
	duration := time.Duration(seconds) * time.Second
	days := int(duration.Hours() / 24)
	hours := int(duration.Hours()) % 24
	minutes := int(duration.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	} else if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	} else {
		return fmt.Sprintf("%dm", minutes)
	}
}
