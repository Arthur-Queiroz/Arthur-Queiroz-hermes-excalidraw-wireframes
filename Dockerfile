# syntax=docker/dockerfile:1.7

FROM excalidraw/excalidraw@sha256:4542f30bea392b833822d0e7db4fa2220e6706ca962c082add2665159fa91758 AS excalidraw-assets

FROM golang@sha256:111d79159b2326f7e80c4a4706e1ba166acb0e2611df853955f3621828cd49e8 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/wireframes .
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12@sha256:b7ebc675e6df3e26840de28a9d119969806b5542902111bfd111964d0930c08a
WORKDIR /app
COPY --from=build /out/wireframes /app/wireframes
COPY --from=excalidraw-assets /usr/share/nginx/html /app/excalidraw
COPY --chown=65532:65532 --from=build /out/data /data
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/app/wireframes"]
