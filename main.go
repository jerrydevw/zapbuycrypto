package main

import (
	"bytes"
	"cloud.google.com/go/logging"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"log"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL    = "https://api.binance.com"
	orderAPI   = "/api/v3/order"
	accountAPI = "/api/v3/account"
	BRL        = "BRL"
)

var (
	binanceApiKey    = ""
	binanceSecretKey = ""
	whatsappToken    = ""
	whatsappApiUrl   = ""
	whatsappPhoneId  = ""
	logger           *logging.Logger
)

func handlePanic() {
	if r := recover(); r != nil {
		log.Println("ERROR:", r)
		debug.PrintStack()
		fmt.Println("Stack Trace:\n", string(debug.Stack()))
	}
}

func main() {
	defer handlePanic()
	secret, err := accessSecretVersion("whatsappConfigs")
	if err != nil {
		log.Fatalf("Falha ao acessar o segredo: %v", err)
	}
	if secret != nil {
		whatsappPhoneId = secret.Value["WHATSAPP_PHONE_ID"].(string)
		whatsappToken = secret.Value["WHATSAPP_TOKEN"].(string)
		whatsappApiUrl = secret.Value["WHATSAPP_API_URL"].(string)
	}

	r := gin.Default()

	r.POST("/whatsapp/webhook", handleWhatsAppWebhook)
	r.GET("/whatsapp/webhook", verifyWebhook)

	r.GET("/health-check", healthCheck)

	err = r.Run(":8080")
	if err != nil {
		log.Fatal(err)
	}
}

func healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": 200})
}

func getAccountInfo() (*AccountInfo, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	queryString := "timestamp=" + timestamp
	signature := createSignature(binanceSecretKey, queryString)
	fullURL := fmt.Sprintf("%s%s?%s&signature=%s", baseURL, accountAPI, queryString, signature)

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", binanceApiKey)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := &http.Client{}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Erro ao consultar saldo: %s", string(body))
	}

	var accountInfo AccountInfo
	if err := json.Unmarshal(body, &accountInfo); err != nil {
		return nil, fmt.Errorf("Erro ao decodificar resposta do saldo: %v", err)
	}

	return &accountInfo, nil
}

func buyCrypto(symbol string, fiatAmount float64) map[string]interface{} {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)

	data := url.Values{}
	data.Set("symbol", symbol)
	data.Set("side", "BUY")
	data.Set("type", "MARKET")
	data.Set("quoteOrderQty", fmt.Sprintf("%.2f", fiatAmount))
	data.Set("timestamp", timestamp)

	signature := createSignature(binanceSecretKey, data.Encode())
	data.Set("signature", signature)

	req, err := http.NewRequest("POST", baseURL+orderAPI, strings.NewReader(data.Encode()))
	if err != nil {
		fmt.Println("Erro ao criar requisição de compra:", err)
		return nil
	}
	req.Header.Set("X-MBX-APIKEY", binanceApiKey)
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

func hasSufficientBalance(accountInfo *AccountInfo, asset string, requiredAmount float64) bool {
	for _, balance := range accountInfo.Balances {
		if balance.Asset == asset {
			free, err := strconv.ParseFloat(balance.Free, 64)
			return err == nil && free >= requiredAmount
		}
	}
	return false
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

func verifyWebhook(c *gin.Context) {
	mode := c.Query("hub.mode")
	challenge := c.Query("hub.challenge")
	verifyToken := c.Query("hub.verify_token")

	expectedToken := whatsappToken

	if mode == "subscribe" && verifyToken == expectedToken {
		c.String(http.StatusOK, challenge)
	} else {
		c.String(http.StatusForbidden, "Forbidden")
	}
}

func handleWhatsAppWebhook(c *gin.Context) {
	fmt.Println("recebendo hook whatsapp")
	var req struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Messages []struct {
						From string `json:"from"`
						Text struct {
							Body string `json:"body"`
						} `json:"text"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload inválido"})
		return
	}

	if len(req.Entry) == 0 || len(req.Entry[0].Changes) == 0 || len(req.Entry[0].Changes[0].Value.Messages) == 0 {
		c.JSON(http.StatusOK, gin.H{"status": "Nenhuma mensagem recebida"})
		return
	}

	message := req.Entry[0].Changes[0].Value.Messages[0]
	from := message.From
	body := strings.ToLower(strings.TrimSpace(message.Text.Body))

	secrets, _ := accessSecretVersion(from)
	binanceApiKey = secrets.Value["BINANCE_API_KEY"].(string)
	binanceSecretKey = secrets.Value["BINANCE_SECRET_KEY"].(string)

	switch {
	case strings.Contains(body, "saldo") && strings.Contains(body, "reais"):
		accountInfo, errAccountinfo := getAccountInfo()
		if errAccountinfo != nil {
			replyWhatsApp(from, "Erro ao consultar saldo.")
			return
		}

		brlBalance := 0.0
		for _, balance := range accountInfo.Balances {
			if balance.Asset == BRL {
				freeAmount, err := strconv.ParseFloat(balance.Free, 64)
				if err != nil {
					continue
				}
				brlBalance = freeAmount
				break
			}
		}

		if brlBalance > 0 {
			replyWhatsApp(from, fmt.Sprintf("Seu saldo em reais é: R$ %.2f", brlBalance))
		} else {
			replyWhatsApp(from, "Você não tem saldo disponível em reais.")
		}

	case strings.HasPrefix(body, "comprar"):
		parts := strings.Fields(body)
		if len(parts) != 4 {
			replyWhatsApp(from, "Formato inválido. Use: comprar <valor> em <cripto> (exemplo: comprar 100R$ em BTC)")
			return
		}

		valueStr := strings.Replace(parts[1], "r$", "", -1)
		valueStr = strings.Replace(valueStr, ",", ".", -1)
		amount, err := strconv.ParseFloat(valueStr, 64)
		if err != nil || amount <= 0 {
			replyWhatsApp(from, "O valor para compra deve ser válido e maior que zero.")
			return
		}

		crypto := strings.ToUpper(parts[3])

		if !isTradingPairValid(crypto + BRL) {
			replyWhatsApp(from, fmt.Sprintf("Desculpe, o par de moedas %s/BRL não é suportado.", crypto))
			return
		}

		accountInfo, errAccountInfo := getAccountInfo()
		if errAccountInfo != nil {
			replyWhatsApp(from, "Erro ao validar saldo para compra.")
			return
		}

		if !hasSufficientBalance(accountInfo, BRL, amount) {
			replyWhatsApp(from, "Saldo insuficiente para realizar a compra.")
			return
		}

		orderResponse := buyCrypto(crypto+BRL, amount)
		if orderResponse == nil {
			replyWhatsApp(from, "Erro ao realizar a compra.")
			return
		}

		replyWhatsApp(from, fmt.Sprintf("Compra realizada com sucesso!\nMoeda: %s\nValor: R$ %.2f\nID do Pedido: %v", crypto, amount, orderResponse["orderId"]))

	default:
		replyWhatsApp(from, "Desculpe, não reconheço este comando. Envie 'ajuda' para listar os comandos disponíveis.")
	}

	c.JSON(http.StatusOK, gin.H{"status": "Mensagem processada com sucesso"})
}

func replyWhatsApp(to string, message string) {
	data := map[string]interface{}{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                to,
		"type":              "text",
		"text": map[string]interface{}{
			"preview_url": false,
			"body":        message,
		},
	}

	bytesRepresentation, err := json.Marshal(data)
	if err != nil {
		log.Fatalf("Can't marshal to JSON: %s", err)
	}

	urlApiWhatsApp := fmt.Sprintf("%s/%s/messages", whatsappApiUrl, whatsappPhoneId)

	req, err := http.NewRequest("POST", urlApiWhatsApp, bytes.NewBuffer(bytesRepresentation))
	if err != nil {
		log.Fatalf("Can't create new request: %s", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+whatsappToken)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := &http.Client{}
	responseZap, err := client.Do(req.WithContext(ctx))
	if err != nil {
		log.Printf("Can't send WhatsApp message: %s", err)
	}

	fmt.Println("lendo response:", responseZap.Status)
	defer responseZap.Body.Close()
	bodyBytes, err := io.ReadAll(responseZap.Body)
	if err != nil {
		log.Printf("Error reading response body: %s", err)
	}

	var prettyJSON bytes.Buffer
	err = json.Indent(&prettyJSON, bodyBytes, "", "\t")
	if err != nil {
		log.Printf("Error indenting JSON: %s", err)
	} else {
		fmt.Println("Response:")
		fmt.Println(string(prettyJSON.Bytes()))
	}
}

func isFiat(currency string) bool {
	fiatCurrencies := map[string]bool{
		"BRL": true,
		"USD": true,
		"EUR": true,
	}

	_, isFiat := fiatCurrencies[currency]
	return isFiat
}

type Secret struct {
	Name  string
	Value map[string]interface{}
}

func accessSecretVersion(name string) (*Secret, error) {

	// Create the client.
	ctx := context.Background()
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		log.Fatalf("failed to setup client: %v", err)
	}
	defer client.Close()

	accessRequest := &secretmanagerpb.AccessSecretVersionRequest{
		Name: buildSecretPath(name),
	}

	// Call the API.
	result, err := client.AccessSecretVersion(ctx, accessRequest)
	if err != nil {
		return nil, err
	}

	var secretData map[string]interface{}
	if err := json.Unmarshal(result.Payload.Data, &secretData); err != nil {
		return nil, err
	}

	secret := &Secret{
		Name:  name,
		Value: secretData,
	}

	return secret, nil
}

func buildSecretPath(secretName string) string {
	projectID := "425135792660"
	return fmt.Sprintf("projects/%s/secrets/%s/versions/latest", projectID, secretName)
}
