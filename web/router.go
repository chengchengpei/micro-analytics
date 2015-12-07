package web

import (
    "encoding/json"
    "net/http"
    "net/url"
    "strconv"
    "strings"
    "time"

    "github.com/azer/logger"
    "github.com/gorilla/mux"
    "github.com/oschwald/maxminddb-golang"

    "github.com/GitbookIO/micro-analytics/database"
    "github.com/GitbookIO/micro-analytics/utils"
    "github.com/GitbookIO/micro-analytics/utils/geoip"
    "github.com/GitbookIO/micro-analytics/web/errors"
)

type RouterOpts struct {
    DBManager      *database.DBManager
    Geolite2Reader *maxminddb.Reader
    Version        string
}

func NewRouter(opts RouterOpts) http.Handler {
    // Create the app router
    r := mux.NewRouter()
    dbManager := opts.DBManager
    geolite2 := opts.Geolite2Reader

    var log = logger.New("[Router]")

    /////
    // Welcome
    /////
    r.Path("/").
        Methods("GET").
        HandlerFunc(func(w http.ResponseWriter, req *http.Request) {

        msg := map[string]string{
            "message": "Welcome to analytics !",
            "version": opts.Version,
        }
        render(w, msg, nil)
    })

    /////
    // Query a DB over time
    /////
    r.Path("/{dbName}/time").
        Methods("GET").
        HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        // Get params from URL
        vars := mux.Vars(req)
        dbName := vars["dbName"]

        // Check if DB file exists
        dbExists, err := dbManager.DBExists(dbName)
        if err != nil {
            renderError(w, &errors.InternalError)
            return
        }

        // DB doesn't exist
        if !dbExists {
            renderError(w, &errors.InvalidDatabaseName)
            return
        }

        // Parse request query
        if err := req.ParseForm(); err != nil {
            renderError(w, err)
            return
        }

        // Get timeRange if provided
        startTime := req.Form.Get("start")
        endTime := req.Form.Get("end")
        intervalStr := req.Form.Get("interval")

        // Convert startTime and endTime to a TimeRange
        timeRange, err := database.NewTimeRange(startTime, endTime)
        if err != nil {
            log.Info("Error creating TimeRange %v", err)
            renderError(w, &errors.InvalidTimeFormat)
            return
        }

        // Cast interval to an integer
        // Defaults to 1 day
        interval := 24 * 60 * 60
        if len(intervalStr) > 0 {
            interval, err = strconv.Atoi(intervalStr)
            if err != nil {
                log.Info("Error casting interval to an int %v", err)
                renderError(w, &errors.InvalidInterval)
                return
            }
        }

        // Get DB from manager
        dbManager.RequestDB <- dbName
        db := <-dbManager.SendDB

        // If value is in Cache, return directly
        response, inCache := dbManager.Cache.Get(req.URL.String())
        if inCache {
            dbManager.UnlockDB <- dbName
            render(w, response, nil)
            return
        }

        // Check for unique query parameter to call function accordingly
        var analytics *database.Intervals
        unique := req.Form.Get("unique")

        if strings.Compare(unique, "true") == 0 {
            analytics, err = db.OverTimeUniq(interval, timeRange)
            if err != nil {
                renderError(w, &errors.InternalError)
                return
            }
        } else {
            analytics, err = db.OverTime(interval, timeRange)
            if err != nil {
                renderError(w, &errors.InternalError)
                return
            }
        }

        // Unlock DB
        dbManager.UnlockDB <- dbName

        // Store response in Cache before sending
        dbManager.Cache.Add(req.URL.String(), analytics)

        // Return query result
        render(w, analytics, nil)
    })

    /////
    // Query a DB by property
    /////
    r.Path("/{dbName}/{property}").
        Methods("GET").
        HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        // Map allowed requests w/ columns names in DB schema
        allowedProperties := map[string]string{
            "countries": "countryCode",
            "platforms": "platform",
            "domains":   "refererDomain",
            "events":    "event",
        }
        // Get params from URL
        vars := mux.Vars(req)
        dbName := vars["dbName"]
        property := vars["property"]

        // Check that property is allowed to be queried
        property, ok := allowedProperties[property]
        if !ok {
            renderError(w, &errors.InvalidProperty)
            return
        }

        // Check if DB file exists
        dbExists, err := dbManager.DBExists(dbName)
        if err != nil {
            renderError(w, &errors.InternalError)
            return
        }

        // DB doesn't exist
        if !dbExists {
            renderError(w, &errors.InvalidDatabaseName)
            return
        }

        // Parse request query
        if err := req.ParseForm(); err != nil {
            renderError(w, err)
            return
        }

        // Get timeRange if provided
        startTime := req.Form.Get("start")
        endTime := req.Form.Get("end")

        timeRange, err := database.NewTimeRange(startTime, endTime)
        if err != nil {
            log.Info("Error creating TimeRange %v", err)
            renderError(w, &errors.InvalidTimeFormat)
            return
        }

        // Get DB from manager
        dbManager.RequestDB <- dbName
        db := <-dbManager.SendDB

        // If value is in Cache, return directly
        response, inCache := dbManager.Cache.Get(req.URL.String())
        if inCache {
            dbManager.UnlockDB <- dbName
            render(w, response, nil)
            return
        }

        // Check for unique query parameter to call function accordingly
        var analytics *database.AggregateList
        unique := req.Form.Get("unique")

        if strings.Compare(unique, "true") == 0 {
            analytics, err = db.GroupByUniq(property, timeRange)
            if err != nil {
                renderError(w, &errors.InternalError)
                return
            }
        } else {
            analytics, err = db.GroupBy(property, timeRange)
            if err != nil {
                renderError(w, &errors.InternalError)
                return
            }
        }

        // Unlock DB
        dbManager.UnlockDB <- dbName

        // Store response in Cache before sending
        dbManager.Cache.Add(req.URL.String(), analytics)

        // Return query result
        render(w, analytics, nil)
    })

    /////
    // Full query a DB
    /////
    r.Path("/{dbName}").
        Methods("GET").
        HandlerFunc(func(w http.ResponseWriter, req *http.Request) {

        // Parse form data
        if err := req.ParseForm(); err != nil {
            renderError(w, err)
            return
        }

        // Get dbName from URL
        vars := mux.Vars(req)
        dbName := vars["dbName"]

        // Check if DB file exists
        dbExists, err := dbManager.DBExists(dbName)
        if err != nil {
            renderError(w, &errors.InternalError)
            return
        }

        // DB doesn't exist
        if !dbExists {
            renderError(w, &errors.InvalidDatabaseName)
            return
        }

        // Parse request query
        if err := req.ParseForm(); err != nil {
            renderError(w, err)
            return
        }

        // Get timeRange if provided
        startTime := req.Form.Get("start")
        endTime := req.Form.Get("end")

        timeRange, err := database.NewTimeRange(startTime, endTime)
        if err != nil {
            log.Info("Error creating TimeRange %v", err)
            renderError(w, &errors.InvalidTimeFormat)
            return
        }

        // Get DB from manager
        dbManager.RequestDB <- dbName
        db := <-dbManager.SendDB

        // If value is in Cache, return directly
        response, inCache := dbManager.Cache.Get(req.URL.String())
        if inCache {
            dbManager.UnlockDB <- dbName
            render(w, response, nil)
            return
        }

        // Return query result
        analytics, err := db.Query(timeRange)
        if err != nil {
            renderError(w, &errors.InternalError)
            return
        }

        // Unlock DB
        dbManager.UnlockDB <- dbName

        // Store response in Cache before sending
        dbManager.Cache.Add(req.URL.String(), analytics)

        render(w, analytics, nil)
    })

    /////
    // Push analytics to a DB
    /////
    r.Path("/{dbName}").
        Methods("POST").
        HandlerFunc(func(w http.ResponseWriter, req *http.Request) {

        // Get dbName from URL
        vars := mux.Vars(req)
        dbName := vars["dbName"]

        // Parse JSON POST data
        postData := PostData{}
        jsonDecoder := json.NewDecoder(req.Body)
        err := jsonDecoder.Decode(&postData)

        // Invalid JSON
        if err != nil {
            renderError(w, &errors.InvalidJSON)
            return
        }

        // Create Analytic to inject in DB
        analytic := database.Analytic{
            Time:  time.Now(),
            Event: postData.Event,
            Path:  postData.Path,
            Ip:    postData.Ip,
        }

        // Set time from POST data if passed
        if len(postData.Time) > 0 {
            analytic.Time, err = time.Parse(time.RFC3339, postData.Time)
        }

        // Get referer from headers
        refererHeader := postData.Headers["referer"]
        if referrerURL, err := url.ParseRequestURI(refererHeader); err == nil {
            analytic.RefererDomain = referrerURL.Host
        }

        // Get platform from headers
        analytic.Platform = utils.Platform(postData.Headers["user-agent"])

        // Get countryCode from GeoIp
        analytic.CountryCode, err = geoip.GeoIpLookup(geolite2, postData.Ip)

        // Get DB from manager
        dbManager.RequestDB <- dbName
        db := <-dbManager.SendDB

        // Insert data if everything's OK
        if err = db.Insert(analytic); err != nil {
            renderError(w, &errors.InsertFailed)
            return
        }

        log.Info("Successfully inserted analytic: %#v", analytic)

        // Unlock DB
        dbManager.UnlockDB <- dbName

        render(w, nil, nil)
    })

    /////
    // Push analytics as-is to a DB
    /////
    r.Path("/{dbName}/special").
        Methods("POST").
        HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        // Get dbName from URL
        vars := mux.Vars(req)
        dbName := vars["dbName"]

        // Parse JSON POST data
        postData := PostAnalytic{}
        jsonDecoder := json.NewDecoder(req.Body)
        err := jsonDecoder.Decode(&postData)

        // Invalid JSON
        if err != nil {
            renderError(w, &errors.InvalidJSON)
            return
        }

        // Create Analytic to inject in DB
        analytic := database.Analytic{
            Time:          time.Unix(int64(postData.Time), 0),
            Event:         postData.Event,
            Path:          postData.Path,
            Ip:            postData.Ip,
            Platform:      postData.Platform,
            RefererDomain: postData.RefererDomain,
            CountryCode:   postData.CountryCode,
        }

        // Get DB from manager
        dbManager.RequestDB <- dbName
        db := <-dbManager.SendDB

        // Insert data
        if err = db.Insert(analytic); err != nil {
            renderError(w, &errors.InsertFailed)
            return
        }

        log.Info("Successfully inserted analytic: %#v", analytic)

        // Unlock DB
        dbManager.UnlockDB <- dbName

        render(w, nil, nil)
    })

    /////
    // Delete a DB
    /////
    r.Path("/{dbName}").
        Methods("DELETE").
        HandlerFunc(func(w http.ResponseWriter, req *http.Request) {

        // Get dbName from URL
        vars := mux.Vars(req)
        dbName := vars["dbName"]

        // Check if DB file exists
        dbExists, err := dbManager.DBExists(dbName)
        if err != nil {
            renderError(w, &errors.InternalError)
            return
        }

        // DB doesn't exist
        if !dbExists {
            renderError(w, &errors.InvalidDatabaseName)
            return
        }

        // Delete full DB directory
        err = dbManager.DeleteDB(dbName)
        render(w, nil, err)
    })

    return r
}
