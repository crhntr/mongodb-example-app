package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	dbClient := databaseClient(
		context.Background(),
		databaseOptions(),
	)

	database := "example"
	if env := os.Getenv("DATABASE"); env != "" {
		database = env
	}
	log.Printf("using database %q", database)
	db := dbClient.Database(database)

	mux := http.NewServeMux()

	mux.Handle("/", handleIndex(db))
	mux.Handle("/collection", handleCollection(db))
	mux.Handle("/document", handleDocument(db))

	err := http.ListenAndServe(":" +os.Getenv("PORT"), mux)
	if err != nil {
		log.Fatal(err)
	}
}

// ================================================================
// HTTP Handlers

//go:embed index.gohtml
var indexHTML string

func handleIndex(db *mongo.Database) http.HandlerFunc {
	tmpl := template.Must(template.New("").Parse(indexHTML))

	return func(res http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), time.Second * 5)
		defer cancel()

		cursor, err := db.ListCollections(ctx, bson.D{})
		if err != nil {
			log.Println("ERROR serveIndex; db.ListCollections", err)
			http.Error(res, "failed to list collections", http.StatusInternalServerError)
			return
		}

		var data struct {
			DatabaseName string
			CollectionNames []string
		}

		for cursor.Next(ctx) {
			if err := cursor.Err(); err != nil {
				log.Println("ERROR serveIndex; cursor.Next", err)
				http.Error(res, "failed to list collections", http.StatusInternalServerError)
				return
			}
			data.CollectionNames = append(data.CollectionNames,
				cursor.Current.Lookup("name").StringValue(),
			)
		}

		res.WriteHeader(http.StatusOK)
		err = tmpl.Execute(res, data)
		if err != nil {
			log.Println("ERROR template.Execute", err)
		}
	}
}

//go:embed collection.gohtml
var collectionHTML string

func handleCollection(db *mongo.Database) http.HandlerFunc {
	tmpl := template.Must(template.New("").Parse(collectionHTML))

	return func(res http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), time.Second * 5)
		defer cancel()

		findOptions := options.Find()

		name := req.URL.Query().Get("name")
		if name == "" {
			http.Error(res, "missing name query parameter", http.StatusBadRequest)
			return
		}

		if param := req.URL.Query().Get("skip"); param != "" {
			n, err := strconv.Atoi(param)
			if err != nil || n < 0 {
				http.Error(res, "invalid skip param", http.StatusBadRequest)
				return
			}
			findOptions.SetSkip(int64(n))
		}

		col := db.Collection(name)

		var (
			data struct {
				DatabaseName string
				CollectionName string
				DocumentCount int64
				IDs []primitive.ObjectID
			}
			err error
		)
		data.DatabaseName = db.Name()
		data.CollectionName = name

		data.DocumentCount, err = col.CountDocuments(ctx, bson.D{})
		if err != nil {
			http.Error(res, "failed to count documents", http.StatusBadRequest)
			return
		}

		cursor, err := col.Find(ctx, bson.D{}, findOptions)
		if err != nil {
			http.Error(res, "failed to query documents", http.StatusBadRequest)
			return
		}

		data.IDs = make([]primitive.ObjectID, 0, cursor.RemainingBatchLength())
		for cursor.Next(ctx) {
			f := cursor.Current.Lookup("_id")
			id, ok := f.ObjectIDOK()
			if !ok {
				http.Error(res, fmt.Sprintf("unsupported ID type %s", f.Type), http.StatusBadRequest)
				return
			}
			data.IDs = append(data.IDs, id)
		}

		res.WriteHeader(http.StatusOK)
		err = tmpl.Execute(res, data)
		if err != nil {
			log.Println("ERROR template.Execute", err)
		}
	}
}

//go:embed document.gohtml
var documentHTML string

func handleDocument(db *mongo.Database) http.HandlerFunc {
	tmpl := template.Must(template.New("").Parse(documentHTML))

	return func(res http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), time.Second * 5)
		defer cancel()

		collection := req.URL.Query().Get("collection")
		if collection == "" {
			http.Error(res, "missing collection query parameter", http.StatusBadRequest)
			return
		}
		idStr := req.URL.Query().Get("id")
		if idStr == "" {
			http.Error(res, "missing id query parameter", http.StatusBadRequest)
			return
		}
		id, err := primitive.ObjectIDFromHex(idStr)
		if err != nil {
			http.Error(res, "invalid object ID", http.StatusBadRequest)
			return
		}

		var document bson.M
		err = db.Collection(collection).FindOne(ctx, bson.D{
			{Key: "_id", Value: id},
		}).Decode(&document)
		if err != nil {
			log.Println(err)
			http.Error(res, "failed to decode document", http.StatusInternalServerError)
			return
		}

		var data struct {
			DatabaseName string
			CollectionName string
			ID primitive.ObjectID
			Document string
		}
		data.DatabaseName = db.Name()
		data.CollectionName = collection
		data.ID = id

		result, err := json.MarshalIndent(document, "", "\t")
		if err != nil {
			log.Println(err)
			http.Error(res, "failed to encode document", http.StatusInternalServerError)
			return
		}
		data.Document = string(result)

		res.WriteHeader(http.StatusOK)
		err = tmpl.Execute(res, data)
		if err != nil {
			log.Println("ERROR template.Execute", err)
		}
	}
}

// ================================================================
// Database client configuration

func databaseOptions() *options.ClientOptions {
	mongoURL := "mongodb://localhost:27017"
	if env := os.Getenv("MONGODB_URL"); env != "" {
		mongoURL = env
	}
	return options.Client().ApplyURI(mongoURL)
}

func databaseClient(ctx context.Context, opts *options.ClientOptions) *mongo.Client {
	const (
		connectTimeout = time.Second*10
		pingTimeout    = time.Second*10
	)

	dbClient, err := mongo.NewClient(opts)
	if err != nil {
		panic(err)
	}

	log.Println("connecting to database")
	ctx, closeConnectCTX := context.WithTimeout(ctx, connectTimeout)
	defer closeConnectCTX()
	if err := dbClient.Connect(ctx); err != nil {
		panic(err)
	}

	log.Println("pinging database")
	ctx, closePingCTX := context.WithTimeout(ctx, pingTimeout)
	defer closePingCTX()
	err = dbClient.Ping(ctx, nil)
	if err != nil {
		panic(err)
	}

	return dbClient
}
