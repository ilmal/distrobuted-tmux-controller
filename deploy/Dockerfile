FROM alpine:3.20
COPY dtc /usr/local/bin/dtc
RUN chmod +x /usr/local/bin/dtc
EXPOSE 7331
ENTRYPOINT ["/usr/local/bin/dtc", "hub"]
