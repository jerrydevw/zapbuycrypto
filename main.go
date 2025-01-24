package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	baseURL    = "https://api.binance.com"
	orderAPI   = "/api/v3/order"
	accountAPI = "/api/v3/account"
	BRL        = "BRL"
	USD        = "USD"
	EUR        = "EUR"
)

var apiKey = os.Getenv("BINANCE_API_KEY")
var secretKey = os.Getenv("BINANCE_SECRET_KEY")

func main() {
	if apiKey == "" || secretKey == "" {
		fmt.Println("Erro: As variáveis de ambiente BINANCE_API_KEY e BINANCE_SECRET_KEY devem estar configuradas.")
		return
	}

	r := gin.Default()

	r.GET("/saldo", func(c *gin.Context) {
		accountInfo := getAccountInfo()
		if accountInfo == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Erro ao consultar saldo"})
			return
		}

		fiatBalances := []map[string]interface{}{}
		for _, balance := range accountInfo.Balances {
			if isFiat(balance.Asset) {
				freeAmount, err := strconv.ParseFloat(balance.Free, 64)
				if err != nil {
					fmt.Printf("Erro ao converter saldo de %s: %v\n", balance.Asset, err)
					continue
				}
				if freeAmount > 0 {
					fiatBalances = append(fiatBalances, map[string]interface{}{
						"asset":  balance.Asset,
						"amount": freeAmount,
					})
				}
			}
		}

		if len(fiatBalances) == 0 {
			c.JSON(http.StatusOK, gin.H{"message": "Nenhum saldo disponível em moedas fiduciárias"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"fiat_balances": fiatBalances})
	})

	r.POST("/comprar", func(c *gin.Context) {
		var req struct {
			Crypto string  `json:"crypto" binding:"required"`
			Amount float64 `json:"amount" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Parâmetros inválidos"})
			return
		}

		if req.Amount <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "O valor para compra deve ser maior que zero"})
			return
		}

		symbol := fmt.Sprintf("%s%s", req.Crypto, BRL)
		orderResponse := buyCrypto(symbol, req.Amount)
		if orderResponse == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Erro ao realizar a compra"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"order_details": orderResponse})
	})

	r.Run(":8080")
}

func isFiat(asset string) bool {
	return asset == USD || asset == EUR || asset == BRL
}

func getAccountInfo() *AccountInfo {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	queryString := "timestamp=" + timestamp
	signature := createSignature(secretKey, queryString)
	fullURL := fmt.Sprintf("%s%s?%s&signature=%s", baseURL, accountAPI, queryString, signature)

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		fmt.Println("Erro ao criar requisição para saldo:", err)
		return nil
	}
	req.Header.Set("X-MBX-APIKEY", apiKey)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := &http.Client{}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		fmt.Println("Erro ao consultar saldo:", err)
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Erro ao ler resposta do saldo:", err)
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Erro ao consultar saldo: %s\n", string(body))
		return nil
	}

	var accountInfo AccountInfo
	if err := json.Unmarshal(body, &accountInfo); err != nil {
		fmt.Println("Erro ao decodificar resposta do saldo:", err)
		return nil
	}
	return &accountInfo
}

func buyCrypto(symbol string, fiatAmount float64) map[string]interface{} {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)

	data := url.Values{}
	data.Set("symbol", symbol)
	data.Set("side", "BUY")
	data.Set("type", "MARKET")
	data.Set("quoteOrderQty", fmt.Sprintf("%.2f", fiatAmount)) // Quantidade em BRL
	data.Set("timestamp", timestamp)

	signature := createSignature(secretKey, data.Encode())
	data.Set("signature", signature)

	req, err := http.NewRequest("POST", baseURL+orderAPI, strings.NewReader(data.Encode()))
	if err != nil {
		fmt.Println("Erro ao criar requisição de compra:", err)
		return nil
	}
	req.Header.Set("X-MBX-APIKEY", apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Erro ao executar a compra:", err)
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Erro ao ler resposta da compra:", err)
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Erro ao executar a compra: %s\n", string(body))
		return nil
	}

	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		fmt.Println("Erro ao decodificar resposta da compra:", err)
		return nil
	}
	return response
}

func createSignature(secretKey, data string) string {
	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

type AccountInfo struct {
	Balances []struct {
		Asset string `json:"asset"`
		Free  string `json:"free"`
	} `json:"balances"`
}
