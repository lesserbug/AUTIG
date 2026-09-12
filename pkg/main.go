package main

import (
	"SpeedFair_simplify/pkg/diagnostics"
	"SpeedFair_simplify/pkg/network"
	ofo "SpeedFair_simplify/pkg/ofo"
	"SpeedFair_simplify/pkg/types"
	"context"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"flag" // <<< 确保 'flag' 被导入
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func init() {
	gob.Register(&types.Transaction{})
	gob.Register(&types.LocalOrder{})
	gob.Register(&types.VerifiableFairOrderFragment{})
	gob.Register(&benchmarkReady{})
	gob.Register(&benchmarkStart{})
	gob.Register(&benchmarkFragmentCommit{})
	gob.Register(&benchmarkVerified{})
	gob.Register(&benchmarkFinish{})
}

// --- 将这些常量作为命令行标志的默认值 ---
const (
	TOTAL_TRANSACTIONS_DEFAULT         = 20000
	TX_SUBMISSION_RATE_PER_SEC_DEFAULT = 700
	TX_SIZE_BYTES_DEFAULT              = 512
	LO_GENERATION_INTERVAL_MS_DEFAULT  = 150
	LO_MAX_TX_COUNT_DEFAULT            = 200
	SIMULATION_DURATION_SEC_DEFAULT    = 10
)

func main() {
	// --- 1. 添加新的命令行标志，并使用常量作为默认值 ---
	var (
		configFile  = flag.String("config", "config.json", "JSON config file for node addresses")
		nodeList    = flag.String("nodes", "", "Comma-separated list of node IDs to run on this instance")
		cpuProfile  = flag.Bool("cpuprofile", false, "Enable CPU profiling for this instance")
		stageTiming = flag.Bool("stage-timing", false, "Log per-fragment benchmark stage timing")
		faultCount  = flag.Uint64("f", 2, "Number of tolerated faulty replicas")
		gamma       = flag.Float64("gamma", 0.90, "Fairness parameter gamma")

		// <<< 新增的标志，用于控制批次大小 >>>
		loInterval  = flag.Int("lo-interval", LO_GENERATION_INTERVAL_MS_DEFAULT, "Interval in milliseconds for generating local orders")
		loSize      = flag.Int("lo-size", LO_MAX_TX_COUNT_DEFAULT, "Maximum number of transactions in one LocalOrder")
		txRate      = flag.Int("tx-rate", TX_SUBMISSION_RATE_PER_SEC_DEFAULT, "Transaction submission rate (tx/s)")
		txSize      = flag.Int("tx-size", TX_SIZE_BYTES_DEFAULT, "Canonical transaction size in bytes (minimum 16)")
		simDuration = flag.Int("sim-duration", SIMULATION_DURATION_SEC_DEFAULT, "Simulation duration in seconds")
	)
	flag.Parse()
	diagnostics.Enable(*stageTiming)
	revision, modified, goVersion := "unknown", "unknown", "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		goVersion = info.GoVersion
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value
			}
		}
	}
	log.Printf("BENCHMARK BUILD revision=%s modified=%s go=%s", revision, modified, goVersion)
	log.Printf("BENCHMARK DIAGNOSTICS stage_timing=%t cpu_profile=%t", *stageTiming, *cpuProfile)
	if *txSize < 16 {
		log.Fatal("Transaction size must be at least 16 bytes.")
	}

	if *cpuProfile {
		f, err := os.Create(fmt.Sprintf("cpu_profile_nodes_%s.pprof", strings.Replace(*nodeList, ",", "_", -1)))
		if err != nil {
			log.Fatalf("Create CPU profile: %v", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatalf("Start CPU profile: %v", err)
		}
		defer pprof.StopCPUProfile()
	}

	// 从配置文件读取节点信息
	appConfig, config, totalNodesFromConfig := readJSONConfig(*configFile)
	nodesToRun := parseNodeList(*nodeList)
	if len(nodesToRun) == 0 {
		log.Fatal("No nodes specified. Use -nodes flag.")
	}
	authenticator, authContext, err := loadFileAuthenticator(appConfig, nodesToRun)
	if err != nil {
		log.Fatalf("Failed to configure AUTIG authentication: %v", err)
	}
	genesis, err := loadProtocolGenesis(appConfig)
	if err != nil {
		log.Fatalf("Failed to configure AUTIG genesis: %v", err)
	}

	maliciousThresholdID := totalNodesFromConfig - *faultCount

	// 打印时，确保使用从配置文件中读取到的总节点数
	fmt.Printf("Starting UTIG-based nodes: %v\n", nodesToRun)
	// <<< 修正点: 使用 totalNodesFromConfig >>>
	fmt.Printf("System params: N=%d, F=%d, Gamma=%.2f\n", totalNodesFromConfig, *faultCount, *gamma)
	fmt.Printf("Malicious replicas are assumed to be IDs >= %d\n", maliciousThresholdID) // 打印提示信息
	fmt.Printf("Workload params: TxRate=%d/s, TxSize=%dB, LO-Interval=%dms, LO-Size=%d\n", *txRate, *txSize, *loInterval, *loSize)
	fmt.Printf("Simulation duration: %d seconds\n\n", *simDuration)

	// --- MODIFIED: 创建根上下文和信号监听器 ---
	// 1. 创建一个可以被取消的根 context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 确保在 main 函数退出时，所有协程都能收到取消信号

	// 2. 启动一个协程来监听中断信号 (Ctrl+C)
	shutdownSignal := make(chan os.Signal, 1)
	signal.Notify(shutdownSignal, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-shutdownSignal
		log.Printf("\nReceived signal: %s. Initiating graceful shutdown...", sig)
		cancel() // 当收到信号时，调用 cancel() 来通知所有协程关闭
	}()

	// The experiment timeout starts only after every replica handler is ready.
	runDistributedMode(ctx, nodesToRun, maliciousThresholdID, config, totalNodesFromConfig, *faultCount, *gamma, *loInterval, *loSize, *txRate, *txSize, *simDuration, authenticator, authContext, genesis)
}

// <<< MODIFIED: 函数签名接收 maliciousThresholdID >>>
func runDistributedMode(rootCtx context.Context, nodeIDs []uint64, maliciousThresholdID uint64, config map[uint64]string, totalNodes, faultCount uint64, gamma float64, loIntervalMs, loSize, txRate, txSize, simDuration int, authenticator types.Authenticator, authContext types.AuthContext, genesis types.ProtocolGenesis) {
	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()

	var wg sync.WaitGroup
	experimentStart := make(chan struct{})
	localStarted := make(chan struct{}, len(nodeIDs))
	localInitFailed := make(chan struct{}, len(nodeIDs))
	var experimentCtx context.Context
	submissionDone := make(chan struct{})

	services := make(map[uint64]*ofo.OFOService)
	var servicesMu sync.Mutex
	var txSubmitterNet network.NetworkInterface
	isLeaderInstance := false
	for _, id := range nodeIDs {
		if id == authContext.LeaderID {
			isLeaderInstance = true
			break
		}
	}

	for _, nodeID := range nodeIDs {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			admission := newMemoryTransactionAdmission()
			netConfig := network.NetworkConfig{ReplicaID: id, ReplicaAddr: config}
			net, err := network.NewDistributedNetwork(netConfig)
			if err != nil {
				log.Printf("Node %d failed to create network: %v", id, err)
				localInitFailed <- struct{}{}
				return
			}
			defer net.Stop()

			log.Printf("Node %d waiting for peers to connect...", id)
			if err := net.WaitForPeers(30 * time.Second); err != nil {
				log.Printf("FATAL: Node %d could not connect to all peers, shutting down. Error: %v", id, err)
				localInitFailed <- struct{}{}
				return
			}

			if id == authContext.LeaderID && isLeaderInstance {
				servicesMu.Lock()
				txSubmitterNet = net
				servicesMu.Unlock()
			}

			// --- 关键修改：判断当前节点是否为恶意节点 ---
			isMalicious := id >= maliciousThresholdID
			if isMalicious {
				log.Printf("Node %d is starting as a MALICIOUS replica.", id)
			} else {
				log.Printf("Node %d is starting as an HONEST replica.", id)
			}

			var hosting *benchmarkHostingAdapter
			var candidateHandler ofo.CandidateHandler
			if id == authContext.LeaderID {
				hosting = &benchmarkHostingAdapter{network: net, replicaCount: totalNodes, faultCount: faultCount, leaderID: authContext.LeaderID, ready: make(chan struct{}), verified: make(chan network.Message, 2*totalNodes)}
				candidateHandler = func(fragment *types.VerifiableFairOrderFragment, digest [32]byte) {
					select {
					case <-experimentStart:
					case <-ctx.Done():
						return
					}
					hosting.Propose(experimentCtx, fragment, digest)
				}
			}
			service, err := ofo.NewOFOService(id, totalNodes, faultCount, gamma, net, loSize, &loIntervalMs, isMalicious, authenticator, admission, authContext, genesis, candidateHandler)
			if err != nil {
				log.Printf("Node %d failed to create AUTIG service: %v", id, err)
				localInitFailed <- struct{}{}
				return
			}
			defer service.Stop()
			net.Benchmark = service.Benchmark
			if hosting != nil {
				hosting.leader = service
				close(hosting.ready)
			}
			start := make(chan struct{})
			adapter := &benchmarkNodeAdapter{
				service:      service,
				replicaID:    id,
				replicaCount: totalNodes,
				leaderID:     authContext.LeaderID,
				network:      net,
				start:        start,
				readySenders: make(map[uint64]struct{}),
				hosting:      hosting,
				finished:     make(chan struct{}),
			}
			net.Register(id, adapter.HandleMessage)

			servicesMu.Lock()
			services[id] = service
			servicesMu.Unlock()

			readyTicker := time.NewTicker(200 * time.Millisecond)
		readyLoop:
			for {
				net.Send(network.Message{Type: "BenchmarkReady", From: id, To: authContext.LeaderID, Payload: &benchmarkReady{ReplicaID: id}})
				select {
				case <-start:
					break readyLoop
				case <-readyTicker.C:
				case <-ctx.Done():
					readyTicker.Stop()
					return
				}
			}
			readyTicker.Stop()
			localStarted <- struct{}{}
			select {
			case <-experimentStart:
			case <-ctx.Done():
				return
			}

			loGenTicker := time.NewTicker(time.Duration(loIntervalMs) * time.Millisecond)
			defer loGenTicker.Stop()
			loCtx := experimentCtx
			if hosting == nil {
				// Followers keep producing evidence until the leader's finish marker;
				// their independently received Start must not shorten its window.
				var stopLO context.CancelFunc
				loCtx, stopLO = context.WithTimeout(ctx, time.Duration(simDuration+30)*time.Second)
				defer stopLO()
			}

		loGenLoop:
			for {
				select {
				case <-loGenTicker.C:
					if loCtx.Err() != nil {
						break loGenLoop
					}
					service.GenerateAndSendLocalOrder()
				case <-loCtx.Done():
					break loGenLoop
				case <-adapter.finished:
					break loGenLoop
				}
			}
			if hosting != nil {
				service.Stop()
				<-submissionDone
				seq, state, digest := service.CommittedContext()
				log.Printf("BENCHMARK STATE replica=%d seq=%d state=%x fragment=%x", id, seq, state, digest)
				for peer := uint64(0); peer < totalNodes; peer++ {
					if peer != id && !net.Send(network.Message{Type: "BenchmarkFinish", From: id, To: peer, Payload: &benchmarkFinish{}}) {
						log.Printf("BENCHMARK INVALID: finish send failed to replica %d", peer)
					}
				}
				finishCtx, stopFinish := context.WithTimeout(ctx, 30*time.Second)
				finishedPeers := map[uint64]bool{id: true}
			finishLoop:
				for uint64(len(finishedPeers)) < totalNodes {
					select {
					case message := <-hosting.verified:
						if _, ok := message.Payload.(*benchmarkFinish); ok {
							finishedPeers[message.From] = true
						}
					case <-finishCtx.Done():
						log.Println("BENCHMARK INVALID: final state barrier did not complete")
						break finishLoop
					}
				}
				stopFinish()
			} else {
				// Keep receiving the final committed prefix after measurement stops.
				select {
				case <-adapter.finished:
					if !net.Send(network.Message{Type: "BenchmarkFinish", From: id, To: authContext.LeaderID, Payload: &benchmarkFinish{}}) {
						log.Printf("BENCHMARK INVALID: finish acknowledgement failed at replica %d", id)
					}
				case <-loCtx.Done():
					log.Printf("BENCHMARK INVALID: replica %d did not receive finish", id)
				}
			}
			log.Printf("Node %d shutting down...", id)
		}(nodeID)
	}

	for range nodeIDs {
		select {
		case <-localStarted:
		case <-localInitFailed:
			cancel()
			wg.Wait()
			return
		case <-ctx.Done():
			wg.Wait()
			return
		}
	}
	measurementDuration := time.Duration(simDuration) * time.Second
	experimentStartTime := time.Now()
	experimentCtx, experimentCancel := context.WithDeadline(ctx, experimentStartTime.Add(measurementDuration))
	defer experimentCancel()
	if isLeaderInstance {
		services[authContext.LeaderID].MeasurementDeadline = experimentStartTime.Add(measurementDuration)
	}
	for _, service := range services {
		service.Benchmark.Begin(experimentStartTime, experimentStartTime.Add(measurementDuration))
	}
	// CPU is per process (one node per EC2 in remote runs), including all goroutines.
	cpuStart, cpuStartErr := diagnostics.ProcessCPUTime()
	cpuWallStart := time.Now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-experimentCtx.Done()
		cpuEnd, cpuEndErr := diagnostics.ProcessCPUTime()
		seconds := time.Since(cpuWallStart).Seconds()
		report := map[string]interface{}{"replicas": nodeIDs, "sample_seconds": seconds}
		if cpuStartErr != nil || cpuEndErr != nil {
			report["cpu_seconds"] = nil
			report["error"] = fmt.Sprintf("start=%v end=%v", cpuStartErr, cpuEndErr)
		} else {
			report["cpu_seconds"] = (cpuEnd - cpuStart).Seconds()
			report["cpu_percent_one_core"] = 100 * (cpuEnd - cpuStart).Seconds() / seconds
		}
		diagnostics.PrintJSON("CPU", report)
	}()
	close(experimentStart)

	var submittedTxCount int32
	var failedTxSends int64
	if isLeaderInstance {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(submissionDone)
			servicesMu.Lock()
			net := txSubmitterNet
			servicesMu.Unlock()
			submitTransactions(experimentCtx, net, authContext.LeaderID, totalNodes, txRate, txSize, &submittedTxCount, &failedTxSends, experimentStartTime)
		}()
	}

	// 监控协程也需要等待
	wg.Add(1)
	go func() {
		defer wg.Done()
		monitorSystem(experimentCtx, services, &servicesMu, isLeaderInstance, authContext.LeaderID, &submittedTxCount, &failedTxSends, submissionDone, experimentStartTime)
	}()

	wg.Wait()
	for _, id := range nodeIDs {
		diagnostics.PrintJSON("MECHANISM", services[id].Benchmark.Report(id))
	}
	fmt.Println("\nAll nodes on this instance have shut down.")
}

// transactionTarget is the cumulative number due since the measurement start.
// Split seconds and nanoseconds to avoid overflowing rate * elapsed nanoseconds
// in ordinary, long benchmark runs.
func transactionTarget(rate int, elapsed time.Duration) int64 {
	if rate <= 0 || elapsed <= 0 {
		return 0
	}
	return int64(elapsed/time.Second)*int64(rate) + int64(elapsed%time.Second)*int64(rate)/int64(time.Second)
}

func submitTransactions(ctx context.Context, net network.NetworkInterface, senderID, totalNodes uint64, txRate, txSize int, submittedCounter *int32, failedSendCounter *int64, startTime time.Time) {
	if txRate <= 0 {
		log.Println("Transaction submission rate is 0, no transactions will be submitted.")
		return
	}

	// Ticks only wake the scheduler; they do not represent individual arrivals.
	// Above 1000 tx/s, one wakeup can produce a small group of due transactions.
	interval := time.Second / time.Duration(txRate)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var txCounter int64
	deadline, hasDeadline := ctx.Deadline()
	log.Println("Transaction submission started.")
	defer log.Println("Transaction submission stopping...")
	for {
		now := time.Now()
		if ctx.Err() != nil || (hasDeadline && !now.Before(deadline)) {
			return
		}
		expected := transactionTarget(txRate, now.Sub(startTime))
		if txCounter >= expected {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
			continue
		}
		for txCounter < expected {
			submittedAt := time.Now()
			if ctx.Err() != nil || (hasDeadline && !submittedAt.Before(deadline)) {
				return
			}
			// Count real submission attempts, never scheduled-but-uncreated work.
			// Keep the real timestamp so catch-up does not backdate latency.
			txCounter++
			atomic.AddInt32(submittedCounter, 1)
			canonical := make([]byte, txSize)
			binary.BigEndian.PutUint64(canonical[:8], uint64(txCounter))
			binary.BigEndian.PutUint64(canonical[8:16], rand.Uint64())

			tx := types.Transaction{
				ID:             types.TransactionID(canonical),
				CanonicalBytes: canonical,
				SubmissionTime: submittedAt,
			}

			for i := uint64(0); i < totalNodes; i++ {
				if !net.Send(network.Message{Type: "Transaction", From: senderID, To: i, Payload: &tx}) {
					atomic.AddInt64(failedSendCounter, 1)
				}
			}
		}
		// Recompute immediately: synchronous fanout may itself have accrued debt.
		// Finish an already-counted transaction's fanout, but never create new
		// transactions after the cutoff, even if the target has not been reached.
	}
}

// <<< MODIFIED: monitorSystem 现在也由 WaitGroup 管理，并监听 context >>>
func monitorSystem(ctx context.Context, services map[uint64]*ofo.OFOService, servicesMu *sync.Mutex, isLeaderInstance bool, leaderID uint64, submittedCounter *int32, failedSendCounter *int64, submissionDone <-chan struct{}, startTime time.Time) {
	if isLeaderInstance {
		servicesMu.Lock()
		leaderService := services[leaderID]
		servicesMu.Unlock()
		monitorWithLeader(ctx, leaderService, submittedCounter, failedSendCounter, submissionDone, startTime)
	} else {
		monitorWithoutLeader(ctx, services, servicesMu)
	}
}

func monitorWithLeader(ctx context.Context, leaderService *ofo.OFOService, submittedCounter *int32, failedSendCounter *int64, submissionDone <-chan struct{}, startTime time.Time) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	log.Println("Leader monitor started.")
	for {
		select {
		case <-ticker.C:
			finalizedCount, avgLatency, latencyCount := leaderService.GetMeasurementStats()
			utigSize := leaderService.GetUTIGNodeCount()
			submittedCount := atomic.LoadInt32(submittedCounter)
			elapsed := time.Since(startTime).Seconds()
			if elapsed < 1 {
				elapsed = 1
			}
			tps := float64(finalizedCount) / elapsed

			// <<< ADDED >>> 获取并格式化延迟数据
			latencyStr := "N/A"
			if latencyCount > 0 {
				latencyStr = avgLatency.Round(time.Millisecond).String()
			}

			// <<< MODIFIED >>> 在输出中加入延迟
			fmt.Printf("\r[LEADER] Time: %.1fs, Submitted: %d, Finalized: %d, UTIG: %d, TPS: %.2f, Mean Completed Latency: %s",
				elapsed, submittedCount, finalizedCount, utigSize, tps, latencyStr)

		case <-ctx.Done():
			fmt.Println("\nSimulation time ended.")
			<-submissionDone
			finalizedCount, finalAvgLatency, latencySamples := leaderService.GetMeasurementStats()
			totalSubmitted := atomic.LoadInt32(submittedCounter)
			failedSends := atomic.LoadInt64(failedSendCounter)
			deadline, _ := ctx.Deadline()
			if time.Now().Before(deadline) {
				log.Println("BENCHMARK INVALID: measurement cancelled before deadline")
				return
			}
			printFinalReport(deadline.Sub(startTime), finalizedCount, totalSubmitted, failedSends, finalAvgLatency, latencySamples)
			return
		}
	}
}

func monitorWithoutLeader(ctx context.Context, services map[uint64]*ofo.OFOService, servicesMu *sync.Mutex) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	log.Println("Follower monitor started.")
	for {
		select {
		case <-ticker.C:
			var report strings.Builder
			report.WriteString("\r[FOLLOWERS] ")
			servicesMu.Lock()
			for id, service := range services {
				finalized := service.GetFinalizedCount()
				report.WriteString(fmt.Sprintf("Node %d: {Finalized: %d} ", id, finalized))
			}
			servicesMu.Unlock()
			fmt.Print(report.String())
		case <-ctx.Done():
			fmt.Println("\nFollower monitor shutting down.")
			return
		}
	}
}

func printFinalReport(cutoffDuration time.Duration, finalizedCount int, totalSubmitted int32, failedSends int64, avgLatency time.Duration, latencySamples int64) {
	fmt.Println("\n\n--- FINAL RESULTS ---")
	if int64(finalizedCount) != latencySamples {
		fmt.Printf("Benchmark Result Invalid: finalized=%d latency_samples=%d\n", finalizedCount, latencySamples)
		fmt.Println("--- END FINAL RESULTS ---")
		return
	}
	seconds := cutoffDuration.Seconds()
	throughput := float64(finalizedCount) / seconds
	offeredRate := float64(totalSubmitted) / seconds
	outstanding := int64(totalSubmitted) - latencySamples
	completionRatio := 0.0
	if totalSubmitted > 0 {
		completionRatio = float64(latencySamples) / float64(totalSubmitted)
	}
	fmt.Printf("Measurement Duration: %s\n", cutoffDuration)
	fmt.Printf("Total Submitted: %d\n", totalSubmitted)
	fmt.Printf("Total Finalized: %d\n", finalizedCount)
	fmt.Printf("Average TPS:     %.2f\n", throughput)
	fmt.Printf("Actual Offered Rate: %.2f\n", offeredRate)
	fmt.Printf("Locally Failed Transaction Send Attempts: %d\n", failedSends)

	// <<< ADDED >>> 打印平均延迟
	latencyStr := "N/A"
	if avgLatency > 0 {
		latencyStr = avgLatency.Round(time.Millisecond).String()
	}
	fmt.Printf("Mean Completed-Transaction Latency: %s\n", latencyStr)
	fmt.Printf("Latency Samples: %d\n", latencySamples)
	fmt.Printf("Outstanding: %d\n", outstanding)
	fmt.Printf("Completion Ratio: %.6f\n", completionRatio)
	fmt.Println("--- END FINAL RESULTS ---")
}

type Config struct {
	Nodes                 map[string]string `json:"nodes"`
	Epoch                 uint64            `json:"epoch"`
	LeaderID              uint64            `json:"leader_id"`
	ReplicaPublicKeys     map[string]string `json:"replica_public_keys"`
	ReplicaPrivateKeys    map[string]string `json:"replica_private_keys"`
	LeaderPublicKey       string            `json:"leader_public_key"`
	LeaderPrivateKey      string            `json:"leader_private_key"`
	GenesisStateID        string            `json:"genesis_state_id"`
	GenesisFragmentDigest string            `json:"genesis_fragment_digest"`
}

func readJSONConfig(filename string) (Config, map[uint64]string, uint64) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatalf("Failed to read config file %s: %v", filename, err)
	}
	var jsonConfig Config
	if err := json.Unmarshal(data, &jsonConfig); err != nil {
		log.Fatalf("Failed to parse config file: %v", err)
	}
	config := make(map[uint64]string)
	var maxID uint64 = 0
	for k, v := range jsonConfig.Nodes {
		id, _ := strconv.ParseUint(k, 10, 64)
		if id > maxID {
			maxID = id
		}
		config[id] = v
	}
	return jsonConfig, config, maxID + 1
}
func parseNodeList(nodeList string) []uint64 {
	if nodeList == "" {
		return []uint64{}
	}
	parts := strings.Split(nodeList, ",")
	nodes := make([]uint64, 0, len(parts))
	for _, part := range parts {
		id, _ := strconv.ParseUint(strings.TrimSpace(part), 10, 64)
		nodes = append(nodes, id)
	}
	return nodes
}
