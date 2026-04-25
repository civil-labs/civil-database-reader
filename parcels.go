package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	parcelsv1 "github.com/civil-labs/civil-api-go/civil/mesh/parcels/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ParcelServer struct {
	db     *pgxpool.Pool
	logger *slog.Logger
}

func (s *ParcelServer) GetParcelAttribute(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelAttributeRequest],
) (*connect.Response[parcelsv1.GetParcelAttributeResponse], error) {

	s.logger.Debug("received GetParcelAttribute request")

	if req.Msg.GetParcelId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	}

	if req.Msg.GetAttributeName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("attribute name is required"))
	}

	// Strip hidden newlines/spaces and force lowercase so sanitize result will matche Postgres's default behavior
	cleanAttr := strings.TrimSpace(req.Msg.GetAttributeName())
	cleanAttr = strings.ToLower(cleanAttr)

	safeColumn := pgx.Identifier{cleanAttr}.Sanitize()

	// Safely inject the sanitized identifier into the query string
	query := fmt.Sprintf(`SELECT %s::text FROM parcels WHERE parcel_id = $1`, safeColumn)

	s.logger.Debug("executing database query", slog.String("query", query))

	var value *string
	err := s.db.QueryRow(ctx, query, req.Msg.GetParcelId()).Scan(&value)
	if err != nil {
		// Gracefully handle the "column does not exist" error

		s.logger.Debug("GetParcelAttribute query failed", slog.Any("error", err))

		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42703" { // 42703 is the Postgres code for undefined_column
			msg := fmt.Sprintf("attribute %s does not exist", req.Msg.AttributeName)
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
		}

		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
		}

		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to retrieve attribute"))
	}

	res := &parcelsv1.GetParcelAttributeResponse{
		AttributeValue: *value,
	}
	return connect.NewResponse(res), nil
}

func (s *ParcelServer) GetParcelProperty(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelPropertyRequest],
) (*connect.Response[parcelsv1.GetParcelPropertyResponse], error) {

	if req.Msg.ParcelId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	}
	if req.Msg.PropertyName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("property name is required"))
	}

	query := `SELECT properties ->> $2 FROM parcels WHERE id = $1`

	// Execute Query
	var value *string // Use a pointer to handle nulls if the key doesn't exist in the JSON
	err := s.db.QueryRow(ctx, query, req.Msg.ParcelId, req.Msg.PropertyName).Scan(&value)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("database error"))
	}

	if value == nil {
		msg := fmt.Sprintf("property %s not found for parcel %s", req.Msg.PropertyName, req.Msg.ParcelId)
		return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	}

	res := &parcelsv1.GetParcelPropertyResponse{
		PropertyValue: *value,
	}
	return connect.NewResponse(res), nil
}

func (s *ParcelServer) GetParcel(
	ctx context.Context,
	req *connect.Request[parcelsv1.GetParcelRequest],
) (*connect.Response[parcelsv1.GetParcelResponse], error) {

	if req.Msg.ParcelId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("parcel ID is required"))
	}

	query := `SELECT * FROM parcels WHERE id = $1`

	// Execute Query
	var value *string // Use a pointer to handle nulls if the key doesn't exist in the JSON
	err := s.db.QueryRow(ctx, query, req.Msg.ParcelId).Scan(&value)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("parcel not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("database error"))
	}

	if value == nil {
		msg := fmt.Sprintf("parcel %s not found", req.Msg.ParcelId)
		return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	}

	res := &parcelsv1.GetParcelResponse{
		PropertyValue: *value,
	}

	return connect.NewResponse(res), nil
}
