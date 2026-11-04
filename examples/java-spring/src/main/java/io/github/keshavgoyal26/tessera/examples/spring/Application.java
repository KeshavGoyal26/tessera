package io.github.keshavgoyal26.tessera.examples.spring;

import io.github.keshavgoyal26.tessera.RoomServiceClient;
import io.github.keshavgoyal26.tessera.WebhookReceiver;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.context.annotation.Bean;

/** Sketch of a Spring Boot backend that integrates with Tessera. */
@SpringBootApplication
public class Application {

  public static void main(String[] args) {
    SpringApplication.run(Application.class, args);
  }

  @Bean
  RoomServiceClient roomServiceClient(
      @Value("${tessera.url}") String url,
      @Value("${tessera.api-key}") String apiKey,
      @Value("${tessera.api-secret}") String apiSecret) {
    return RoomServiceClient.create(url, apiKey, apiSecret);
  }

  @Bean
  WebhookReceiver webhookReceiver(
      @Value("${tessera.api-key}") String apiKey,
      @Value("${tessera.api-secret}") String apiSecret) {
    return new WebhookReceiver(apiKey, apiSecret);
  }
}
