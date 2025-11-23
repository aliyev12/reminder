package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- 1. Data Models ---

// Reminder represents the database model
type Reminder struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Title       string     `json:"title"`
	Date        time.Time  `json:"date"`     // Start time for one-time events
	Location    string     `json:"location"` // Stored as JSON string
	Description string     `json:"description"`
	Reminders   string     `json:"reminders"` // Stored as JSON string (contacts)
	Alerts      string     `json:"alerts"`    // Stored as JSON string (array of milliseconds)
	IsRecurring bool       `json:"is_recurring"`
	Recurrence  string     `json:"recurrence"` // CRON expression
	StartDate   time.Time  `json:"start_date"`
	EndDate     *time.Time `json:"end_date"` // Optional/Nullable
}

// Global DB variable
var db *gorm.DB

// --- 2. Database Setup & Helper Functions ---

func initDB() {
	var err error
	// Connect to local SQLite file
	db, err = gorm.Open(sqlite.Open("reminders.db"), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Auto-migrate the Reminder model
	db.AutoMigrate(&Reminder{})
	fmt.Println("Database connected and migrated.")
}

// simpleAuthMiddleware checks for X-API-Key header
func simpleAuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Skip auth for GET /reminders (implied by "except GET /reminders" requirement usually,
		// but strict requirement says "except GET /reminders" logic.
		// We will follow strict requirement: GET /reminders is public, others protected.)
		if c.Request().Method == http.MethodGet && c.Path() == "/reminders" {
			return next(c)
		}

		key := c.Request().Header.Get("X-API-Key")
		if key != "SECRET_KEY" {
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

	// Validation logic
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
	// Ticker runs every 60 seconds
	ticker := time.NewTicker(60 * time.Second)

	// Cron parser for recurring logic
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	go func() {
		for {
			select {
			case t := <-ticker.C:
				// Run logic
				checkReminders(t, parser)
			}
		}
	}()
}

func checkReminders(now time.Time, parser cron.Parser) {
	var reminders []Reminder
	// Fetch all reminders
	db.Find(&reminders)

	for _, r := range reminders {
		var alertOffsets []int64
		// Parse alerts JSON string into slice of ints
		if r.Alerts != "" {
			_ = json.Unmarshal([]byte(r.Alerts), &alertOffsets)
		}

		// Logic for determining if we should alert
		for _, offsetMs := range alertOffsets {
			var eventTime time.Time

			if r.IsRecurring {
				// Calculate next activation based on cron
				// We need to find the schedule relative to "Now"
				// But to be accurate, we look for the instance that might have just happened.

				schedule, err := parser.Parse(r.Recurrence)
				if err != nil {
					log.Printf("Error parsing cron for %s: %v", r.Title, err)
					continue
				}

				// Simple MVP logic: Get the next scheduled time.
				// Note: Handling "alerts before recurring events" is complex logic.
				// For this MVP, we will check if (NextEventTime - offset) is roughly NOW.

				nextRun := schedule.Next(now)
				eventTime = nextRun
			} else {
				// One time event
				eventTime = r.Date
			}

			// Calculate Alert Time
			alertDuration := time.Duration(offsetMs) * time.Millisecond
			alertTime := eventTime.Add(-alertDuration)

			// Check if alertTime was within the last 60 seconds
			// Since the ticker runs every 60s, we look for a match in the window [now-60s, now]
			diff := now.Sub(alertTime)

			// If the alert time is in the past (positive diff) and within 1 minute
			shouldTrigger := diff >= 0 && diff < 60*time.Second

			if shouldTrigger {
				// Alert Action: Log to console
				fmt.Printf("\n========================================\n")
				fmt.Printf("ALERT TRIGGERED!\n")
				fmt.Printf("Title: %s\n", r.Title)
				fmt.Printf("Event Time: %s\n", eventTime.Format(time.RFC3339))
				fmt.Printf("Message: %s\n", r.Description)
				fmt.Printf("Contact Info: %s\n", r.Reminders)
				fmt.Printf("========================================\n\n")
			}
		}
	}
}

// --- Main Entry Point ---

func main() {
	// Initialize DB
	initDB()

	// Start Background Worker
	startScheduler()

	// Initialize Echo
	e := echo.New()

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(simpleAuthMiddleware) // Custom Auth

	// Routes
	e.GET("/reminders", getAllReminders)
	e.GET("/reminders/:id", getReminder)
	e.POST("/reminders", createReminder)
	e.PUT("/reminders/:id", updateReminder)
	e.DELETE("/reminders/:id", deleteReminder)

	// Start Server
	e.Logger.Fatal(e.Start(":8080"))
}
