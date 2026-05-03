# keywords: spring springboot kotlin jvm
# description: Java Gradle multi-stage: gradle:jdk21 builder with Gradle cache, temurin JRE runtime
FROM gradle:8-jdk21-alpine AS builder
WORKDIR /app
RUN --mount=type=cache,target=/root/.gradle \
    --mount=type=bind,source=build.gradle,target=build.gradle \
    --mount=type=bind,source=settings.gradle,target=settings.gradle \
    gradle dependencies --no-daemon
COPY . .
RUN --mount=type=cache,target=/root/.gradle \
    gradle jar --no-daemon

FROM eclipse-temurin:21-jre-alpine
WORKDIR /app
COPY --from=builder /app/build/libs/*.jar app.jar
RUN addgroup -S app && adduser -S app -G app
USER app
EXPOSE 8080
CMD ["java", "-jar", "app.jar"]
