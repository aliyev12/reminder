package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv" // --- NEW: Library to read .env files
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/robfig/cron/v3"
	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- 1. Data Models ---

type Reminder struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Title       string     `json:"title"`
	Date        time.Time  `json:"date"`
	Location    string     `json:"location"`
	Description string     `json:"description"`
	Reminders   string     `json:"reminders"`
	Alerts      string     `json:"alerts"`
	IsRecurring bool       `json:"is_recurring"`
	Recurrence  string     `json:"recurrence"`
	StartDate   time.Time  `json:"start_date"`
	EndDate     *time.Time `json:"end_date"`
}

type Contact struct {
	Mode    string `json:"mode"`
	Address string `json:"address"`
}

var db *gorm.DB

// --- 2. Database Setup & Middleware ---

func initDB() {
	var err error
	db, err = gorm.Open(sqlite.Open("reminders.db"), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
	db.AutoMigrate(&Reminder{})
	fmt.Println("Database connected and migrated.")
}

func simpleAuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Public endpoint
		if c.Request().Method == http.MethodGet && c.Path() == "/reminders" {
			return next(c)
		}

		// --- NEW: Fetch secret from environment ---
		requiredKey := os.Getenv("APP_API_KEY")
		if requiredKey == "" {
			// Fallback or error if env not set
			log.Println("Warning: APP_API_KEY not set in .env")
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Server misconfiguration"})
		}

		key := c.Request().Header.Get("X-API-Key")
		if key != requiredKey {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid or missing API Key"})
		}
		return next(c)
	}
}

// --- 3. Request Handlers ---

func createReminder(c echo.Context) error {
	r := new(Reminder)
	if err := c.Bind(r); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
	}
	if r.IsRecurring {
		if r.Recurrence == "" || r.StartDate.IsZero() {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Recurring events must have recurrence string and start_date"})
		}
	}
	if result := db.Create(&r); result.Error != nil {
		return c.JSON(http.StatusInternalServerError, result.Error.Error())
	}
	return c.JSON(http.StatusCreated, r)
}

func getAllReminders(c echo.Context) error {
	var reminders []Reminder
	if result := db.Find(&reminders); result.Error != nil {
		return c.JSON(http.StatusInternalServerError, result.Error.Error())
	}
	return c.JSON(http.StatusOK, reminders)
}

func getReminder(c echo.Context) error {
	id := c.Param("id")
	var reminder Reminder
	if result := db.First(&reminder, id); result.Error != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Reminder not found"})
	}
	return c.JSON(http.StatusOK, reminder)
}

func updateReminder(c echo.Context) error {
	id := c.Param("id")
	var reminder Reminder
	if result := db.First(&reminder, id); result.Error != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Reminder not found"})
	}
	if err := c.Bind(&reminder); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid input"})
	}
	db.Save(&reminder)
	return c.JSON(http.StatusOK, reminder)
}

func deleteReminder(c echo.Context) error {
	id := c.Param("id")
	if result := db.Delete(&Reminder{}, id); result.Error != nil {
		return c.JSON(http.StatusInternalServerError, result.Error.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Deleted"})
}

// --- 4. Background Worker / Scheduler ---

func startScheduler() {
	ticker := time.NewTicker(60 * time.Second)
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	go func() {
		for {
			select {
			case t := <-ticker.C:
				checkReminders(t, parser)
			}
		}
	}()
}

func checkReminders(now time.Time, parser cron.Parser) {
	var reminders []Reminder
	db.Find(&reminders)

	for _, r := range reminders {
		var alertOffsets []int64
		if r.Alerts != "" {
			_ = json.Unmarshal([]byte(r.Alerts), &alertOffsets)
		}

		for _, offsetMs := range alertOffsets {
			var eventTime time.Time

			if r.IsRecurring {
				schedule, err := parser.Parse(r.Recurrence)
				if err != nil {
					log.Printf("Error parsing cron for %s: %v", r.Title, err)
					continue
				}
				eventTime = schedule.Next(now)
			} else {
				eventTime = r.Date
			}

			alertDuration := time.Duration(offsetMs) * time.Millisecond
			alertTime := eventTime.Add(-alertDuration)

			diff := now.Sub(alertTime)
			shouldTrigger := diff >= 0 && diff < 60*time.Second

			if shouldTrigger {
				fmt.Printf("ALERT TRIGGERED for '%s'! Sending notifications...\n", r.Title)

				var contacts []Contact
				if err := json.Unmarshal([]byte(r.Reminders), &contacts); err != nil {
					log.Println("Error parsing reminders/contacts JSON:", err)
					continue
				}

				for _, contact := range contacts {
					if contact.Mode == "email" {
						sendEmail(contact.Address, "Reminder: "+r.Title, r.Description)
					}
				}
			}
		}
	}
}

func sendEmail(toAddress, subject, content string) {
	// --- NEW: Fetch SendGrid secrets from environment ---
	apiKey := os.Getenv("SENDGRID_API_KEY")
	fromEmail := os.Getenv("SENDGRID_FROM_EMAIL")

	if apiKey == "" || fromEmail == "" {
		log.Println("Error: SendGrid API Key or From Email not set in .env")
		return
	}

	from := mail.NewEmail("Reminder App", fromEmail)
	to := mail.NewEmail("User", toAddress)
	message := mail.NewSingleEmail(from, subject, to, content, content)

	client := sendgrid.NewSendClient(apiKey)
	response, err := client.Send(message)

	if err != nil {
		log.Println("Failed to send email:", err)
	} else {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			fmt.Printf("Email sent successfully to %s\n", toAddress)
		} else {
			fmt.Printf("Failed to send email. Status: %d, Body: %s\n", response.StatusCode, response.Body)
		}
	}
}

// --- Main Entry Point ---

func main() {
	// --- NEW: Load .env file ---
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: .env file not found. Relying on system environment variables.")
	}

	initDB()
	startScheduler()

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(simpleAuthMiddleware)

	e.GET("/reminders", getAllReminders)
	e.GET("/reminders/:id", getReminder)
	e.POST("/reminders", createReminder)
	e.PUT("/reminders/:id", updateReminder)
	e.DELETE("/reminders/:id", deleteReminder)

	// Start Server
	e.Logger.Fatal(e.Start(":8080"))
}
