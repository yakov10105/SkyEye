# Code Documentation and Style Guide

## 🚨 CRITICAL: NO COMMENTS IN CODE

The primary principle is to write self-documenting code. Comments are a code smell that often indicates that the code itself is not clear. Refactor the code to make it clearer before resorting to a comment.

- ❌ **NO inline comments** to explain what code does.
- ❌ **NO comment blocks** to explain logic flow.
- ✅ **Write self-documenting code** with clear and descriptive variable and method names.
- ✅ **Extract complex logic** into well-named functions/methods.
- ✅ **ONLY use idiomatic documentation comments** for public APIs, complex algorithms, or non-obvious logic that cannot be simplified further.
  - For .NET, use `///` XML documentation.
  - For Go, use GoDoc comments.
- ✅ **If you feel the need to add a comment, refactor the code instead.**

### Example - BAD:

```csharp
// Check if account has enough funds
if (account.Balance >= transactionAmount)
{
    // Deduct funds from account
    account.Balance -= transactionAmount;
}
```

### Example - GOOD:

```csharp
if (account.HasSufficientFunds(transactionAmount))
{
    account.DeductFunds(transactionAmount);
}
```

### Test Code Clarity

This "no comments" rule applies to test code as well. Test names should be descriptive enough to explain the scenario being tested.

- ❌ **No comments explaining test logic** - The test name should be self-explanatory.
- ✅ **Use descriptive test method names** that explain the scenario (e.g., `MethodName_Scenario_ExpectedBehavior`).
- ✅ **Use descriptive variable names** (e.g., `expectedBalance`, `invalidPlayerId`).
- ✅ **Ensure clear separation** of Arrange, Act, and Assert sections (or equivalent for table-driven tests).
