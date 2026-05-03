# keywords: mvn spring springboot jvm
# description: Java Maven multi-stage: temurin JDK builder with .m2 cache, temurin JRE runtime
FROM eclipse-temurin:21-jdk-alpine AS builder
WORKDIR /app
RUN --mount=type=cache,target=/root/.m2 \
    --mount=type=bind,source=pom.xml,target=pom.xml \
    mvn dependency:go-offline -B
COPY . .
RUN --mount=type=cache,target=/root/.m2 \
    mvn package -DskipTests -B

FROM eclipse-temurin:21-jre-alpine
WORKDIR /app
COPY --from=builder /app/target/*.jar app.jar
RUN addgroup -S app && adduser -S app -G app
USER app
EXPOSE 8080
CMD ["java", "-jar", "app.jar"]
