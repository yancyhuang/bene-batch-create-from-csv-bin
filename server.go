package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type ValidationResults struct {
	Successful struct {
		Count   int           `json:"count"`
		Results []interface{} `json:"results"`
	} `json:"successful"`
	Errors struct {
		Count   int           `json:"count"`
		Results []interface{} `json:"results"`
	} `json:"errors"`
}

type ValidationError struct {
	AccountName  string
	Row          int
	BankCountry  string
	ErrorSource  string
	ErrorMessage string
	Params       string
}

type BeneficiaryCreateResult struct {
	Successful struct {
		Count   int `json:"count"`
		Results []struct {
			Name string `json:"name"`
			Row  int    `json:"row"`
			ID   string `json:"id"`
		} `json:"results"`
	} `json:"successful"`
	Errors struct {
		Count   int `json:"count"`
		Results []struct {
			Name string                 `json:"name"`
			Data map[string]interface{} `json:"data"`
		} `json:"results"`
	} `json:"errors"`
}

type TokenResponse struct {
	ExpiresAt string `json:"expires_at"`
	Token     string `json:"token"`
}

func main() {
	// 设置子命令
	validateCmd := flag.NewFlagSet("validate", flag.ExitOnError)
	createCmd := flag.NewFlagSet("create", flag.ExitOnError)
	tokenCmd := flag.NewFlagSet("token", flag.ExitOnError)

	// validate 子命令的参数
	inputFile := validateCmd.String("i", "", "Input CSV file path (required)")
	envPath := validateCmd.String("env", ".env", "Path to the .env file (default: .env)")
	// 添加环境参数
	isProd := validateCmd.Bool("prod", false, "Use production environment (default: false)")

	// create 子命令的参数
	createProd := createCmd.Bool("prod", false, "Use production environment (default: false)")

	// token 子命令的参数
	tokenProd := tokenCmd.Bool("prod", false, "Use production environment (default: false)")

	// 添加帮助信息
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  server validate -i <csv_file> [-prod]\n")
		fmt.Fprintf(os.Stderr, "  server create [-prod]\n")
		fmt.Fprintf(os.Stderr, "  server token [-prod]\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  server validate -i ./data/create_payment1.csv\n")
		fmt.Fprintf(os.Stderr, "  server validate -i ./data/create_payment1.csv -prod\n")
		fmt.Fprintf(os.Stderr, "  server create\n")
		fmt.Fprintf(os.Stderr, "  server token\n")
	}

	// 检查是否提供了子命令
	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	// 根据子命令解析不同的参数
	switch os.Args[1] {
	case "token":
		tokenCmd.Parse(os.Args[2:])

		// 加载 .env 文件
		err := godotenv.Load()
		if err != nil {
			fmt.Println("Error loading .env file")
			os.Exit(1)
		}

		// 获取认证信息
		clientID := os.Getenv("CLIENT_ID")
		apiKey := os.Getenv("API_KEY")
		if clientID == "" || apiKey == "" {
			fmt.Println("Missing CLIENT_ID or API_KEY in .env file")
			os.Exit(1)
		}

		token := getAuthToken(clientID, apiKey, *tokenProd)

		// 读取现有的 .env 文件内容
		envContent, err := os.ReadFile(".env")
		if err != nil {
			fmt.Printf("Error reading .env file: %v\n", err)
			os.Exit(1)
		}

		// 将内容转换为字符串并按行分割
		lines := strings.Split(string(envContent), "\n")
		tokenFound := false

		// 查找并更新 AIRWALLEX_TOKEN
		for i, line := range lines {
			if strings.HasPrefix(line, "AIRWALLEX_TOKEN=") {
				lines[i] = "AIRWALLEX_TOKEN=" + token
				tokenFound = true
				break
			}
		}

		// 如果没有找到 AIRWALLEX_TOKEN，则添加它
		if !tokenFound {
			lines = append(lines, "AIRWALLEX_TOKEN="+token)
		}

		// 将更新后的内容写回文件
		err = os.WriteFile(".env", []byte(strings.Join(lines, "\n")), 0644)
		if err != nil {
			fmt.Printf("Error writing to .env file: %v\n", err)
			os.Exit(1)
		}

		// 设置环境变量（当前进程）
		err = os.Setenv("AIRWALLEX_TOKEN", token)
		if err != nil {
			fmt.Printf("Error setting environment variable: %v\n", err)
		}

		fmt.Println(token)

	case "validate":
		validateCmd.Parse(os.Args[2:])
		if *inputFile == "" {
			fmt.Println("Error: -i flag is required for validate command")
			validateCmd.PrintDefaults()
			os.Exit(1)
		}

		// 加载 .env 文件
		err := godotenv.Overload(*envPath)
		if err != nil {
			fmt.Printf("Error loading .env file from %s: %v\n", *envPath, err)
			os.Exit(1)
		}

		// 获取 token
		bearerToken := os.Getenv("AIRWALLEX_TOKEN")
		if bearerToken == "" {
			fmt.Println("Missing AIRWALLEX_TOKEN in .env file")
			os.Exit(1)
		}

		validateBeneficiaries(*inputFile, bearerToken, *isProd)

	case "create":
		createCmd.Parse(os.Args[2:])

		// 加载 .env 文件
		err := godotenv.Overload()
		if err != nil {
			fmt.Println("Error loading .env file")
			os.Exit(1)
		}

		// 获取 token
		bearerToken := os.Getenv("AIRWALLEX_TOKEN")
		if bearerToken == "" {
			fmt.Println("Missing AIRWALLEX_TOKEN in .env file")
			os.Exit(1)
		}

		createBeneficiaries(bearerToken, *createProd)

	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		flag.Usage()
		os.Exit(1)
	}
}

func validateBeneficiaries(csvPath string, bearerToken string, isProd bool) {
	baseURL := "https://api-demo.airwallex.com"
	if isProd {
		baseURL = "https://api.airwallex.com"
	}

	// 读取 CSV 文件
	file, err := os.Open(csvPath)
	if err != nil {
		fmt.Printf("Error opening file %s: %v\n", csvPath, err)
		os.Exit(1)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	headers, err := reader.Read()
	if err != nil {
		panic(err)
	}

	var successfulResults []interface{}
	var validationErrors []ValidationError
	rowNum := 1

	// 处理每一行数据
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		rowNum++

		// 构建嵌套字典
		payload := make(map[string]interface{})
		for i, value := range row {
			buildNestedDict(payload, headers[i], value)
		}

		// 发送 POST 请求
		jsonData, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST",
			baseURL+"/api/v1/beneficiaries/validate",
			bytes.NewBuffer(jsonData))
		if err != nil {
			panic(err)
		}
		// fmt.Println(bearerToken)
		// 设置请求头
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearerToken)
		req.Header.Set("User-Agnet", "awx-support-bene-upload/1.0")

		// 发送请求
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()

		var result map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&result)

		// 检查响应
		if resp.StatusCode == 401 {
			validationErrors = append(validationErrors, ValidationError{
				AccountName:  getAccountName(payload),
				Row:          rowNum,
				BankCountry:  getBankCountry(payload),
				ErrorSource:  "Unauthorized",
				ErrorMessage: "Unauthorized",
				Params:       "",
			})
			continue
		}
		if len(result) == 0 {
			// 成功的情况
			successfulResults = append(successfulResults, payload)
		} else {
			// 处理错误
			for field, errMsg := range result {
				validationErrors = append(validationErrors, ValidationError{
					AccountName:  getAccountName(payload),
					Row:          rowNum,
					BankCountry:  getBankCountry(payload),
					ErrorSource:  field,
					ErrorMessage: fmt.Sprintf("%v", errMsg),
					Params:       "",
				})
			}
		}
	}

	// 保存验证结果
	validationResults := ValidationResults{}
	validationResults.Successful.Count = len(successfulResults)
	validationResults.Successful.Results = successfulResults
	validationResults.Errors.Count = len(validationErrors)

	// 美化打印验证结果
	fmt.Printf("\n=== Validation Summary ===\n")
	fmt.Printf("Successful: %d\n", validationResults.Successful.Count)
	fmt.Printf("Errors: %d\n", validationResults.Errors.Count)

	if len(validationErrors) > 0 {
		fmt.Printf("\n=== Detailed Error Information ===\n")
		var printingRowNumber int
		for _, err := range validationErrors {
			if err.Row != printingRowNumber {
				printingRowNumber = err.Row
				fmt.Printf(
					"---------%s: %d, Account Name: %s, Bank Country: %s----------\n",
					"Row",
					err.Row,
					err.AccountName,
					err.BankCountry)
			}
			// fmt.Printf("\nError #%d:\n", i+1)
			if err.ErrorSource == "message" || err.ErrorSource == "details" || err.ErrorSource == "code" {
				fmt.Printf("* Error Source: %s\n", err.ErrorSource)
				fmt.Printf("  Error Message: %s\n", err.ErrorMessage)
				if err.Params != "" {
					fmt.Printf("Parameters: %s\n", err.Params)
				}
			}
		}
	}

	resultsJson, _ := json.MarshalIndent(validationResults, "", "  ")
	os.WriteFile("validation_results.json", resultsJson, 0644)

	// 写入错误到 CSV
	errorFile, _ := os.Create("validation_errors.csv")
	defer errorFile.Close()

	writer := csv.NewWriter(errorFile)
	writer.Write([]string{"Account Name", "Row", "Bank Country", "Error Source", "Error Message", "Params"})

	for _, err := range validationErrors {
		writer.Write([]string{
			err.AccountName,
			fmt.Sprintf("%d", err.Row),
			err.BankCountry,
			err.ErrorSource,
			err.ErrorMessage,
			err.Params,
		})
	}
	writer.Flush()
}

func createBeneficiaries(bearerToken string, isProd bool) {
	baseURL := "https://api-demo.airwallex.com"
	if isProd {
		baseURL = "https://api.airwallex.com"
	}

	// 读取验证结果文件
	validationResultsFile, err := os.ReadFile("validation_results.json")
	if err != nil {
		fmt.Println("Error reading validation_results.json. Please run validate command first.")
		os.Exit(1)
	}

	var validationResults ValidationResults
	if err := json.Unmarshal(validationResultsFile, &validationResults); err != nil {
		fmt.Println("Error parsing validation results:", err)
		os.Exit(1)
	}

	createResults := BeneficiaryCreateResult{}

	// 处理成功验证的受益人
	for i, result := range validationResults.Successful.Results {
		// 发送创建受益人请求
		jsonData, _ := json.Marshal(result)
		req, err := http.NewRequest("POST",
			baseURL+"/api/v1/beneficiaries/create",
			bytes.NewBuffer(jsonData))
		if err != nil {
			panic(err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearerToken)

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()

		var createResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&createResp)

		if resp.StatusCode == 201 {
			// 成功创建
			createResults.Successful.Count++
			createResults.Successful.Results = append(createResults.Successful.Results, struct {
				Name string `json:"name"`
				Row  int    `json:"row"`
				ID   string `json:"id"`
			}{
				Name: getAccountName(result.(map[string]interface{})),
				Row:  i + 1,
				ID:   createResp["beneficiary_id"].(string),
			})
			fmt.Printf("Successfully created - %s\n", getAccountName(result.(map[string]interface{})))
		} else {
			// 创建失败
			createResults.Errors.Count++
			createResults.Errors.Results = append(createResults.Errors.Results, struct {
				Name string                 `json:"name"`
				Data map[string]interface{} `json:"data"`
			}{
				Name: getAccountName(result.(map[string]interface{})),
				Data: createResp,
			})
			fmt.Printf("Error creating - %s\n", getAccountName(result.(map[string]interface{})))
		}
	}

	// 保存创建结果到文件
	createResultsJson, _ := json.MarshalIndent(createResults, "", "  ")
	os.WriteFile("beneficiary_create_result.json", createResultsJson, 0644)
}

func buildNestedDict(dict map[string]interface{}, path string, value string) {
	parts := strings.Split(path, ".")
	current := dict

	if path == "payment_methods" || path == "transfer_methods" {
		current[path] = strings.Split(value, ",")
		return
	}

	for i, part := range parts {
		if i == len(parts)-1 {
			if value != "" {
				current[part] = value
			}
		} else {
			if _, exists := current[part]; !exists {
				current[part] = make(map[string]interface{})
			}
			current = current[part].(map[string]interface{})
		}
	}
}

func getAccountName(payload map[string]interface{}) string {
	if beneficiary, ok := payload["beneficiary"].(map[string]interface{}); ok {
		if bankDetails, ok := beneficiary["bank_details"].(map[string]interface{}); ok {
			if accountName, ok := bankDetails["account_name"].(string); ok {
				return accountName
			}
		}
	}
	return ""
}

func getBankCountry(payload map[string]interface{}) string {
	if beneficiary, ok := payload["beneficiary"].(map[string]interface{}); ok {
		if bankDetails, ok := beneficiary["bank_details"].(map[string]interface{}); ok {
			if bankCountry, ok := bankDetails["bank_country_code"].(string); ok {
				return bankCountry
			}
		}
	}
	return ""
}

func getAuthToken(clientID, apiKey string, isProd bool) string {
	baseURL := "https://api-demo.airwallex.com"
	if isProd {
		baseURL = "https://api.airwallex.com"
	}

	req, err := http.NewRequest("POST",
		baseURL+"/api/v1/authentication/login",
		nil)
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		os.Exit(1)
	}

	// 设置请求头
	req.Header.Set("x-client-id", clientID)
	req.Header.Set("x-api-key", apiKey)

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Error sending request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		fmt.Printf("Error: unexpected status code %d\n", resp.StatusCode)
		os.Exit(1)
	}

	// 解析响应
	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		fmt.Printf("Error parsing response: %v\n", err)
		os.Exit(1)
	}

	return tokenResp.Token
}
