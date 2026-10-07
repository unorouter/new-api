package middleware

import (
	"net/http"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

type turnstileCheckResponse struct {
	Success bool `json:"success"`
}

func TurnstileCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		if common.TurnstileCheckEnabled {
			response := c.Query("turnstile")
			if response == "" {
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": "Turnstile token is empty",
				})
				c.Abort()
				return
			}
			rawRes, err := http.PostForm("https://challenges.cloudflare.com/turnstile/v0/siteverify", url.Values{
				"secret":   {common.TurnstileSecretKey},
				"response": {response},
				"remoteip": {c.ClientIP()},
			})
			if err != nil {
				common.SysLog(err.Error())
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": err.Error(),
				})
				c.Abort()
				return
			}
			defer rawRes.Body.Close()
			var res turnstileCheckResponse
			err = common.DecodeJson(rawRes.Body, &res)
			if err != nil {
				common.SysLog(err.Error())
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": err.Error(),
				})
				c.Abort()
				return
			}
			if !res.Success {
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": "Turnstile verification failed, please refresh and try again!",
				})
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// TurnstileCheckAnonymous challenges only callers without a dashboard session.
// The OAuth start was the one way to create an account without a challenge (the
// gkotto.com Google Workspace farm, 2026-10-06), while linking a provider from a
// logged-in account has nothing to gain from one.
func TurnstileCheckAnonymous() gin.HandlerFunc {
	check := TurnstileCheck()
	return func(c *gin.Context) {
		if c.GetInt("id") > 0 {
			c.Next()
			return
		}
		check(c)
	}
}
