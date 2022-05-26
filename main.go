package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/joho/godotenv"
)

type ValidatorResponse struct {
	Height string `json:"height"`
	Result struct {
		ID          int    `json:"ID"`
		StartEpoch  int    `json:"startEpoch"`
		EndEpoch    int    `json:"endEpoch"`
		Nonce       int    `json:"nonce"`
		Power       int    `json:"power"`
		PubKey      string `json:"pubKey"`
		Signer      string `json:"signer"`
		LastUpdated string `json:"last_updated"`
		Jailed      bool   `json:"jailed"`
		Accum       int    `json:"accum"`
	} `json:"result"`
	Error string `json:"error"`
}

type StakeUpdateResponse struct {
	Data struct {
		StakeUpdates []struct {
			ID              string `json:"id"`
			ValidatorID     string `json:"validatorId"`
			TotalStaked     string `json:"totalStaked"`
			Block           string `json:"block"`
			Nonce           string `json:"nonce"`
			TransactionHash string `json:"transactionHash"`
			LogIndex        string `json:"logIndex"`
		} `json:"stakeUpdates"`
	} `json:"data"`
}

var (
	HeimdallRestUrl    string
	PolygonSubGraphUrl string
	HeimdallChainId    string
	EthereumRPCUrl     string
)

var ethClient *ethclient.Client

func init() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	EthereumRPCUrl = os.Getenv("ethereum_rpc_url")
	PolygonSubGraphUrl = os.Getenv("polygon_sub_graph_url")
	HeimdallRestUrl = os.Getenv("heimdall_rest_url")
	HeimdallChainId = os.Getenv("heimdall_chain_id")
}

func main() {
	validatorIdString := os.Args[1]
	validatorId, err := strconv.Atoi(validatorIdString)
	if err != nil {
		log.Fatal("Invalid validator id")
	}

	ethClient, err = ethclient.Dial(EthereumRPCUrl)
	if err != nil {
		log.Fatal(err)
	}

	ethereumNonce, err := getEthereumValidatorNonce(validatorId)
	if err != nil {
		fmt.Println("Error getting ethereum nonce for validator: ", validatorId, err)
		return
	}

	for {
		heimdallNonce, err := getHeimdallValidatorNonce(validatorId)
		if err != nil {
			fmt.Println("Error getting heimdall nonce for validator: ", validatorId, err)
			time.Sleep(1 * time.Second)
			continue
		}

		fmt.Println("Ethereum nonce : ", ethereumNonce, " Heimdall nonce : ", heimdallNonce)

		if ethereumNonce > heimdallNonce {
			err = processStakeUpdate(validatorId, heimdallNonce+1)
			if err != nil {
				fmt.Println("Error processing stake update for validator: ", validatorId, err)
				time.Sleep(1 * time.Second)
				continue
			}
		}
		time.Sleep(18 * time.Second)
	}

}

func processStakeUpdate(validatorId int, nonce int) error {
	fmt.Println("Processing stake update for validator : ", validatorId, " nonce : ", nonce)
	data, err := querySubGraph(PolygonSubGraphUrl, getStakeUpdateQuery(validatorId, nonce))
	if err != nil {
		fmt.Println("Error getting stake update from subGraph for validator: ", validatorId, err)
		return err
	}

	var response StakeUpdateResponse
	err = json.Unmarshal(data, &response)
	if err != nil {
		fmt.Println("Error unmarshalling stake update for validator: ", validatorId, err)
		return err
	}

	stakeUpdate := response.Data.StakeUpdates[0]

	blockTime, err := getBlockTime(stakeUpdate.Block)
	if err != nil {
		fmt.Println("Unable to get block time with err : ", err)
		return err
	}

	if time.Since(blockTime) < time.Minute*10 {
		fmt.Println("Block time is less than ten minutes, skipping stake-update")
		return nil
	}

	fmt.Println("heimdallcli", "tx", "staking", "stake-update", "--block-number", stakeUpdate.Block, "--id", stakeUpdate.ValidatorID, "--log-index", stakeUpdate.LogIndex, "--nonce", stakeUpdate.Nonce, "--staked-amount", stakeUpdate.TotalStaked, "--tx-hash", stakeUpdate.TransactionHash, "--chain-id", HeimdallChainId)
	err = exec.Command("heimdallcli", "tx", "staking", "stake-update", "--block-number", stakeUpdate.Block, "--id", stakeUpdate.ValidatorID, "--log-index", stakeUpdate.LogIndex, "--nonce", stakeUpdate.Nonce, "--staked-amount", stakeUpdate.TotalStaked, "--tx-hash", stakeUpdate.TransactionHash, "--chain-id", HeimdallChainId).Run()
	if err != nil {
		fmt.Println("Error running heimdallcli stake update for validator: ", validatorId, err)
		return err
	}
	fmt.Println("--------------------------------------------------------------------------------------------------------------------------")
	return nil
}

func getHeimdallValidatorNonce(validatorId int) (int, error) {
	// Giving mumbai heimdall, Make it configurabel in future
	requestUrl := fmt.Sprintf("%s/staking/validator/%d", HeimdallRestUrl, validatorId)
	response, err := http.Get(requestUrl)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return 0, err
	}

	var responseData ValidatorResponse
	err = json.Unmarshal(data, &responseData)
	if err != nil {
		return 0, err
	}

	if responseData.Error != "" {
		return -1, nil
	}

	return responseData.Result.Nonce, nil
}

func getEthereumValidatorNonce(validatorId int) (int, error) {
	data, err := querySubGraph(PolygonSubGraphUrl, getLatestNonceQuery(validatorId))
	if err != nil {
		return 0, err
	}

	var response StakeUpdateResponse
	err = json.Unmarshal(data, &response)
	if err != nil {
		return 0, err
	}

	if len(response.Data.StakeUpdates) == 0 {
		return 0, nil
	}

	latestValidatorNonce, err := strconv.Atoi(response.Data.StakeUpdates[0].Nonce)
	if err != nil {
		return 0, err
	}

	return latestValidatorNonce, nil
}

func getBlockTime(blockNumber string) (time.Time, error) {
	blockBig, ok := big.NewInt(0).SetString(blockNumber, 10)
	if !ok {
		return time.Time{}, fmt.Errorf("invalid block number: %s", blockNumber)
	}
	block, err := ethClient.BlockByNumber(context.Background(), blockBig)
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(int64(block.Time()), 0), nil
}

// <------------------------------ GRAPH ----------------------------------->

func querySubGraph(grapghUrl string, query []byte) (data []byte, err error) {
	request, err := http.NewRequest("POST", grapghUrl, bytes.NewBuffer(query))
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: time.Second * 10}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	return ioutil.ReadAll(response.Body)
}

func getLatestNonceQuery(validatorId int) []byte {
	query := map[string]string{
		"query": `
		{
			stakeUpdates(first:1, orderBy: nonce, orderDirection : desc, where: {validatorId: ` + strconv.Itoa(validatorId) + `}){
				nonce
		   } 
		}   
		`,
	}

	byteQuery, _ := json.Marshal(query)
	return byteQuery
}

func getStakeUpdateQuery(validatorId int, nonce int) []byte {
	query := map[string]string{
		"query": `
		{
			stakeUpdates(where: {validatorId: ` + strconv.Itoa(validatorId) + `, nonce: ` + strconv.Itoa(nonce) + `}){
				id
				validatorId
				totalStaked
				block
				nonce
				transactionHash
				logIndex
		   } 
		}   
		`,
	}

	byteQuery, _ := json.Marshal(query)
	return byteQuery
}
