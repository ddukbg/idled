package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/briandowns/spinner"
	"github.com/spf13/cobra"
	"github.com/younsl/idled/internal/models"
	"github.com/younsl/idled/pkg/aws"
	"github.com/younsl/idled/pkg/formatter"
	"github.com/younsl/idled/pkg/pricing"
	"github.com/younsl/idled/pkg/utils"
)

// Version information
const (
	Version   = "0.3.0"
	BuildDate = "2025-04-14"
)

var (
	regions           []string
	services          []string
	showVersion       bool
	supportedServices = map[string]bool{
		"ec2":    true,
		"ebs":    true,
		"s3":     true,
		"lambda": true,
		"eip":    true,
	}
)

// Define service descriptions for help text
var serviceDescriptions = map[string]string{
	"ec2":    "Find stopped EC2 instances",
	"ebs":    "Find unattached EBS volumes",
	"s3":     "Find idle S3 buckets",
	"lambda": "Find idle Lambda functions",
	"eip":    "Find unattached Elastic IP addresses",
}

// startResourceSpinner creates and starts a spinner with a message for the given service
func startResourceSpinner(service string) *spinner.Spinner {
	s := spinner.New(spinner.CharSets[9], 200*time.Millisecond)
	s.Suffix = fmt.Sprintf(" Analyzing %s resources ...", service)
	// Don't set FinalMSG here as it will be set dynamically based on scan time
	s.Start()
	return s
}

func main() {
	var showServiceList bool

	rootCmd := &cobra.Command{
		Use:   "idled",
		Short: "CLI tool to find idle AWS resources",
		Long: `idled is a CLI tool that searches for idle AWS resources
and displays the results in a table format.`,
		Run: func(cmd *cobra.Command, args []string) {
			// If version flag is set, print version info and exit
			if showVersion {
				fmt.Printf("idled version %s (built: %s)\n", Version, BuildDate)
				return
			}

			// If list services flag is set, show available services and exit
			if showServiceList {
				fmt.Println("Available services:")

				// Get a sorted list of supported services for consistent output
				var serviceList []string
				for service, isSupported := range supportedServices {
					if isSupported {
						serviceList = append(serviceList, service)
					}
				}
				sort.Strings(serviceList)

				// Print each service with its description
				for _, service := range serviceList {
					description, ok := serviceDescriptions[service]
					if !ok {
						description = "No description available"
					}
					fmt.Printf("  %-8s - %s\n", service, description)
				}

				fmt.Println("\nExample usage:")
				fmt.Printf("  %s --services %s\n", os.Args[0], strings.Join(serviceList[:min(3, len(serviceList))], ","))
				return
			}

			// Use default region if none specified
			if len(regions) == 0 {
				regions = []string{utils.GetDefaultRegion()}
			}

			// Validate regions
			var validRegions []string
			for _, region := range regions {
				if utils.IsValidRegion(region) {
					validRegions = append(validRegions, region)
				} else {
					fmt.Printf("Warning: Skipping invalid region '%s'\n", region)
				}
			}

			if len(validRegions) == 0 {
				fmt.Println("No valid regions specified. Exiting.")
				return
			}

			// Use default service if none specified
			if len(services) == 0 {
				services = []string{"ec2"}
			}

			// Validate services
			for _, service := range services {
				supported, exists := supportedServices[service]
				if !exists {
					fmt.Printf("Warning: Unknown service '%s'\n", service)
					continue
				}
				if !supported {
					fmt.Printf("Warning: Service '%s' is not yet implemented\n", service)
				}
			}

			// Only process supported services
			var activeServices []string
			for _, service := range services {
				if supported, exists := supportedServices[service]; exists && supported {
					activeServices = append(activeServices, service)
				}
			}

			if len(activeServices) == 0 {
				fmt.Println("No supported services specified. Exiting.")
				return
			}

			// Process each service
			for _, service := range activeServices {
				switch service {
				case "ec2":
					processEC2(validRegions)
				case "ebs":
					processEBS(validRegions)
				case "s3":
					processS3(validRegions)
				case "lambda":
					processLambda(validRegions)
				case "eip":
					processEIP(validRegions)
				// Add more services here in the future
				default:
					// This should never happen due to earlier checks
					fmt.Printf("Skipping unsupported service: %s\n", service)
				}
			}

			// Print combined pricing API statistics once after all services are processed
			formatter.PrintPricingAPIStats()
		},
	}

	// Version flag
	rootCmd.Flags().BoolVarP(&showVersion, "version", "v", false, "Show version information")

	// Service list flag (show available services)
	rootCmd.Flags().BoolVarP(&showServiceList, "list-services", "l", false, "List available services")

	// Initialize default regions
	defaultRegions := []string{utils.GetDefaultRegion()}

	// Region flags (long and short forms)
	rootCmd.Flags().StringSliceVarP(&regions, "regions", "r", nil,
		fmt.Sprintf("AWS regions to check (comma separated, default: %s)", strings.Join(defaultRegions, ", ")))

	// Initialize default services
	defaultServices := []string{"ec2"}

	// Service flags (long and short forms)
	rootCmd.Flags().StringSliceVarP(&services, "services", "s", nil,
		fmt.Sprintf("AWS services to check (comma separated, default: %s)", strings.Join(defaultServices, ", ")))

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// processEC2 handles the scanning of EC2 instances
func processEC2(regions []string) {
	fmt.Println("Starting EC2 scan ...")
	scanStartTime := time.Now()

	// Start the spinner
	s := startResourceSpinner("EC2")

	// Slice to store results
	allInstances := make([]struct {
		instances []models.InstanceInfo
		err       error
		region    string
	}, len(regions))

	// Process each region in parallel
	var wg sync.WaitGroup
	for i, region := range regions {
		wg.Add(1)
		go func(idx int, r string) {
			defer wg.Done()

			client, err := aws.NewEC2Client(r)
			if err != nil {
				allInstances[idx].err = err
				allInstances[idx].region = r
				return
			}

			instances, err := client.GetStoppedInstances()
			allInstances[idx].instances = instances
			allInstances[idx].err = err
			allInstances[idx].region = r
		}(i, region)
	}

	wg.Wait()

	// Calculate scan duration
	scanDuration := time.Since(scanStartTime)

	// Process results to get total count
	var allStoppedInstances []models.InstanceInfo
	for _, result := range allInstances {
		if result.err == nil {
			allStoppedInstances = append(allStoppedInstances, result.instances...)
		}
	}

	// Set completion message with scan time and resource count
	s.FinalMSG = fmt.Sprintf("✓ [%d instances found] EC2 resources analyzed - Completed in %.2f seconds\n",
		len(allStoppedInstances), scanDuration.Seconds())
	s.Stop() // Stop the spinner when done

	// Display API init message if any
	if msg := pricing.GetInitMessage(); msg != "" {
		fmt.Println(msg)
	}

	// Process results for errors
	allStoppedInstances = []models.InstanceInfo{} // Reset to re-process

	// Process results from each region
	for _, result := range allInstances {
		if result.err != nil {
			fmt.Printf("Error in region %s: %v\n", result.region, result.err)
			continue
		}
		allStoppedInstances = append(allStoppedInstances, result.instances...)
	}

	// Display as table
	formatter.PrintInstancesTable(allStoppedInstances, scanStartTime, scanDuration)
	formatter.PrintInstancesSummary(allStoppedInstances)
}

// processEBS handles the scanning of available EBS volumes
func processEBS(regions []string) {
	fmt.Println("Starting EBS scan ...")
	scanStartTime := time.Now()

	// Start the spinner
	s := startResourceSpinner("EBS")

	// Slice to store results
	allVolumes := make([]struct {
		volumes []models.VolumeInfo
		err     error
		region  string
	}, len(regions))

	// Process each region in parallel
	var wg sync.WaitGroup
	for i, region := range regions {
		wg.Add(1)
		go func(idx int, r string) {
			defer wg.Done()

			client, err := aws.NewEBSClient(r)
			if err != nil {
				allVolumes[idx].err = err
				allVolumes[idx].region = r
				return
			}

			volumes, err := client.GetAvailableVolumes()
			allVolumes[idx].volumes = volumes
			allVolumes[idx].err = err
			allVolumes[idx].region = r
		}(i, region)
	}

	wg.Wait()

	// Calculate scan duration
	scanDuration := time.Since(scanStartTime)

	// Process results to get total count
	var allAvailableVolumes []models.VolumeInfo
	for _, result := range allVolumes {
		if result.err == nil {
			allAvailableVolumes = append(allAvailableVolumes, result.volumes...)
		}
	}

	// Set completion message with scan time and resource count
	s.FinalMSG = fmt.Sprintf("✓ [%d volumes found] EBS resources analyzed - Completed in %.2f seconds\n",
		len(allAvailableVolumes), scanDuration.Seconds())
	s.Stop() // Stop the spinner when done

	// Display API init message if any
	if msg := pricing.GetInitMessage(); msg != "" {
		fmt.Println(msg)
	}

	// Process results for errors
	allAvailableVolumes = []models.VolumeInfo{} // Reset to re-process

	// Process results from each region
	for _, result := range allVolumes {
		if result.err != nil {
			fmt.Printf("Error in region %s: %v\n", result.region, result.err)
			continue
		}
		allAvailableVolumes = append(allAvailableVolumes, result.volumes...)
	}

	// Display as table with the requested format
	formatter.PrintVolumesTable(allAvailableVolumes, scanStartTime, scanDuration)
	formatter.PrintVolumesSummary(allAvailableVolumes)
}

// processS3 handles the scanning of idle S3 buckets
func processS3(regions []string) {
	fmt.Println("Starting S3 scan ...")
	scanStartTime := time.Now()

	// Start the spinner
	s := startResourceSpinner("S3")

	// Slice to store results
	allBuckets := make([]struct {
		buckets []models.BucketInfo
		err     error
		region  string
	}, len(regions))

	// Process each region in parallel
	var wg sync.WaitGroup
	for i, region := range regions {
		wg.Add(1)
		go func(idx int, r string) {
			defer wg.Done()

			client, err := aws.NewS3Client(r)
			if err != nil {
				allBuckets[idx].err = err
				allBuckets[idx].region = r
				return
			}

			buckets, err := client.GetIdleBuckets()
			allBuckets[idx].buckets = buckets
			allBuckets[idx].err = err
			allBuckets[idx].region = r
		}(i, region)
	}

	wg.Wait()

	// Calculate scan duration
	scanDuration := time.Since(scanStartTime)

	// Process results to get total count
	var allIdleBuckets []models.BucketInfo
	for _, result := range allBuckets {
		if result.err == nil {
			allIdleBuckets = append(allIdleBuckets, result.buckets...)
		}
	}

	// Set completion message with scan time and resource count
	s.FinalMSG = fmt.Sprintf("✓ [%d buckets found] S3 resources analyzed - Completed in %.2f seconds\n",
		len(allIdleBuckets), scanDuration.Seconds())
	s.Stop() // Stop the spinner when done

	// Display API init message if any
	if msg := pricing.GetInitMessage(); msg != "" {
		fmt.Println(msg)
	}

	// Process results for errors
	allIdleBuckets = []models.BucketInfo{} // Reset to re-process

	// Process results from each region
	for _, result := range allBuckets {
		if result.err != nil {
			fmt.Printf("Error in region %s: %v\n", result.region, result.err)
			continue
		}
		allIdleBuckets = append(allIdleBuckets, result.buckets...)
	}

	// Display as table
	formatter.PrintBucketsTable(allIdleBuckets, scanStartTime, scanDuration)
	formatter.PrintBucketsSummary(allIdleBuckets)
}

// processLambda handles the scanning of idle Lambda functions
func processLambda(regions []string) {
	fmt.Println("Starting Lambda scan...")
	scanStartTime := time.Now()

	// Start the spinner
	s := startResourceSpinner("Lambda")

	// Slice to store results
	allFunctions := make([]struct {
		functions []models.LambdaFunctionInfo
		err       error
		region    string
	}, len(regions))

	// Process each region in parallel
	var wg sync.WaitGroup
	for i, region := range regions {
		wg.Add(1)
		go func(idx int, r string) {
			defer wg.Done()

			client, err := aws.NewLambdaClient(r)
			if err != nil {
				allFunctions[idx].err = err
				allFunctions[idx].region = r
				return
			}

			functions, err := client.GetIdleFunctions()
			allFunctions[idx].functions = functions
			allFunctions[idx].err = err
			allFunctions[idx].region = r
		}(i, region)
	}

	wg.Wait()

	// Calculate scan duration
	scanDuration := time.Since(scanStartTime)

	// Process results to get total count
	var allIdleFunctions []models.LambdaFunctionInfo
	for _, result := range allFunctions {
		if result.err == nil {
			allIdleFunctions = append(allIdleFunctions, result.functions...)
		}
	}

	// Set completion message with scan time and resource count
	s.FinalMSG = fmt.Sprintf("✓ [%d functions found] Lambda resources analyzed - Completed in %.2f seconds\n",
		len(allIdleFunctions), scanDuration.Seconds())
	s.Stop() // Stop the spinner when done

	// Process results for errors
	allIdleFunctions = []models.LambdaFunctionInfo{} // Reset to re-process

	// Process results from each region
	for _, result := range allFunctions {
		if result.err != nil {
			fmt.Printf("Error in region %s: %v\n", result.region, result.err)
			continue
		}
		allIdleFunctions = append(allIdleFunctions, result.functions...)
	}

	// Display as table
	formatter.PrintLambdaTable(allIdleFunctions, scanStartTime, scanDuration)
	formatter.PrintLambdaSummary(allIdleFunctions)
}

// processEIP handles the scanning of unattached Elastic IPs
func processEIP(regions []string) {
	fmt.Println("Starting Elastic IP scan ...")
	scanStartTime := time.Now()

	// Start the spinner
	s := startResourceSpinner("Elastic IP")

	// Slice to store results
	allEIPs := make([]struct {
		eips   []models.EIPInfo
		err    error
		region string
	}, len(regions))

	// Process each region in parallel
	var wg sync.WaitGroup
	for i, region := range regions {
		wg.Add(1)
		go func(idx int, r string) {
			defer wg.Done()

			client, err := aws.NewEIPClient(r)
			if err != nil {
				allEIPs[idx].err = err
				allEIPs[idx].region = r
				return
			}

			eips, err := client.GetUnattachedEIPs()
			allEIPs[idx].eips = eips
			allEIPs[idx].err = err
			allEIPs[idx].region = r
		}(i, region)
	}

	wg.Wait()

	// Calculate scan duration
	scanDuration := time.Since(scanStartTime)

	// Process results to get total count
	var allUnattachedEIPs []models.EIPInfo
	for _, result := range allEIPs {
		if result.err == nil {
			allUnattachedEIPs = append(allUnattachedEIPs, result.eips...)
		}
	}

	// Set completion message with scan time and resource count
	s.FinalMSG = fmt.Sprintf("✓ [%d EIPs found] Elastic IP resources analyzed - Completed in %.2f seconds\n",
		len(allUnattachedEIPs), scanDuration.Seconds())
	s.Stop() // Stop the spinner when done

	// Process results for errors
	allUnattachedEIPs = []models.EIPInfo{} // Reset to re-process

	// Process results from each region
	for _, result := range allEIPs {
		if result.err != nil {
			fmt.Printf("Error in region %s: %v\n", result.region, result.err)
			continue
		}
		allUnattachedEIPs = append(allUnattachedEIPs, result.eips...)
	}

	// Display as table
	formatter.PrintEIPsTable(allUnattachedEIPs, scanStartTime, scanDuration)
	formatter.PrintEIPsSummary(allUnattachedEIPs)
}

// min returns the smaller of x or y
func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}
